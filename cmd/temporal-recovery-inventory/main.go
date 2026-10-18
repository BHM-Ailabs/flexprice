// temporal-recovery-inventory reads visibility, authoritative descriptions and
// complete histories. It has no registration, termination or other write RPC.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/encoding/protojson"
)

const query = "ExecutionStatus = 'Running' AND WorkflowType IN ('ProcessSubscriptionBillingWorkflow', 'DraftAndComputeSubscriptionInvoiceWorkflow')"

type reader interface {
	ListWorkflowExecutions(context.Context, *workflowservice.ListWorkflowExecutionsRequest, ...grpc.CallOption) (*workflowservice.ListWorkflowExecutionsResponse, error)
	DescribeWorkflowExecution(context.Context, *workflowservice.DescribeWorkflowExecutionRequest, ...grpc.CallOption) (*workflowservice.DescribeWorkflowExecutionResponse, error)
	GetWorkflowExecutionHistory(context.Context, *workflowservice.GetWorkflowExecutionHistoryRequest, ...grpc.CallOption) (*workflowservice.GetWorkflowExecutionHistoryResponse, error)
}

// SDK defaults log to stdout and could corrupt the JSON manifest. Suppress SDK
// diagnostics (which may also contain connection details); report read failures
// explicitly without raw server errors or payloads.
type quietLogger struct{}

func (quietLogger) Debug(string, ...interface{}) {}
func (quietLogger) Info(string, ...interface{})  {}
func (quietLogger) Warn(string, ...interface{})  {}
func (quietLogger) Error(string, ...interface{}) {}

type scope struct {
	SubscriptionID string `json:"subscription_id"`
	TenantID       string `json:"tenant_id"`
	EnvironmentID  string `json:"environment_id"`
}

type row struct {
	scope
	WorkflowID    string `json:"workflow_id"`
	RunID         string `json:"run_id"`
	WorkflowType  string `json:"workflow_type"`
	Status        string `json:"status"`
	StartTime     string `json:"start_time"`
	HistoryEvents int    `json:"history_events"`
	Decision      string `json:"decision"`
	CanonicalRun  string `json:"canonical_run"` // deliberately empty: retain none
}

type inventory struct {
	Namespace       string         `json:"namespace"`
	Query           string         `json:"query"`
	CapturedAt      string         `json:"captured_at"`
	CanonicalPolicy string         `json:"canonical_policy"`
	Counts          map[string]int `json:"counts"`
	Rows            []row          `json:"rows"`
}

func main() {
	address := flag.String("address", "", "required private Temporal frontend address")
	namespace := flag.String("namespace", "", "required explicit namespace")
	timeout := flag.Duration("timeout", 2*time.Hour, "total read deadline; no partial manifest on failure")
	startedFrom := flag.String("started-from", "", "inclusive RFC3339 StartTime for a resumable time shard")
	startedBefore := flag.String("started-before", "", "exclusive RFC3339 StartTime for a resumable time shard")
	historyDir := flag.String("history-dir", "", "private local directory for complete histories, including full inputs; not an approved manifest")
	flag.Parse()
	if *address == "" || *namespace == "" || *timeout <= 0 {
		fmt.Fprintln(os.Stderr, "--address and --namespace are required; output is a private, unapproved review manifest")
		os.Exit(2)
	}
	shardQuery, err := queryForRange(*startedFrom, *startedBefore)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if *historyDir != "" {
		if err := os.MkdirAll(*historyDir, 0700); err != nil {
			fmt.Fprintln(os.Stderr, "cannot create private history directory")
			os.Exit(1)
		}
		info, err := os.Stat(*historyDir)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			fmt.Fprintln(os.Stderr, "history directory must be private (mode 0700)")
			os.Exit(1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	c, err := client.DialContext(ctx, client.Options{HostPort: *address, Namespace: *namespace, Logger: quietLogger{}})
	if err != nil {
		fmt.Fprintln(os.Stderr, "Temporal connection failed; no inventory produced")
		os.Exit(1)
	}
	defer c.Close()
	result, err := collectShard(ctx, *namespace, c.WorkflowService(), shardQuery, *historyDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(result); err != nil {
		os.Exit(1)
	}
}

func collect(ctx context.Context, namespace string, r reader) (*inventory, error) {
	return collectShard(ctx, namespace, r, query, "")
}

func queryForRange(from, before string) (string, error) {
	q := query
	var lower, upper time.Time
	for _, bound := range []struct{ value, operator string }{{from, ">="}, {before, "<"}} {
		if bound.value == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339Nano, bound.value)
		if err != nil {
			return "", fmt.Errorf("StartTime bounds must be RFC3339 timestamps")
		}
		if bound.operator == ">=" {
			lower = t
		} else {
			upper = t
		}
		q += fmt.Sprintf(" AND StartTime %s '%s'", bound.operator, t.UTC().Format(time.RFC3339Nano))
	}
	if from != "" && before != "" && !lower.Before(upper) {
		return "", fmt.Errorf("--started-from must precede --started-before")
	}
	return q, nil
}

func collectShard(ctx context.Context, namespace string, r reader, shardQuery, historyDir string) (*inventory, error) {
	result := &inventory{Namespace: namespace, Query: shardQuery, CapturedAt: time.Now().UTC().Format(time.RFC3339), CanonicalPolicy: "none; operator approval and stable complete inventory required", Counts: map[string]int{"listed": 0, "subscriptions": 0, "running": 0, "surplus-over-one": 0, "terminate-preactivity": 0, "manual-reconciliation": 0, "closed-no-termination": 0}, Rows: []row{}}
	seen := map[string]bool{}
	groups := map[string]int{}
	var page []byte
	for {
		if err := pace(ctx); err != nil {
			return nil, err
		}
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		list, err := r.ListWorkflowExecutions(callCtx, &workflowservice.ListWorkflowExecutionsRequest{Namespace: namespace, Query: shardQuery, PageSize: 100, NextPageToken: page})
		cancel()
		if err != nil {
			return nil, fmt.Errorf("visibility read failed; no complete inventory produced")
		}
		for _, info := range list.GetExecutions() {
			ex := info.GetExecution()
			if ex.GetWorkflowId() == "" || ex.GetRunId() == "" {
				return nil, fmt.Errorf("missing execution identity; no complete inventory produced")
			}
			key := ex.GetWorkflowId() + "/" + ex.GetRunId()
			if seen[key] {
				continue
			}
			seen[key] = true
			if err := pace(ctx); err != nil {
				return nil, err
			}
			callCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
			description, err := r.DescribeWorkflowExecution(callCtx, &workflowservice.DescribeWorkflowExecutionRequest{Namespace: namespace, Execution: ex})
			cancel()
			if err != nil {
				return nil, fmt.Errorf("authoritative describe failed for %s; no complete inventory produced", key)
			}
			d := description.GetWorkflowExecutionInfo()
			if d.GetStatus() == enumspb.WORKFLOW_EXECUTION_STATUS_UNSPECIFIED || d.GetStartTime() == nil || (d.GetType().GetName() != "ProcessSubscriptionBillingWorkflow" && d.GetType().GetName() != "DraftAndComputeSubscriptionInvoiceWorkflow") {
				return nil, fmt.Errorf("unexpected authoritative description for %s; no complete inventory produced", key)
			}
			x := row{WorkflowID: ex.GetWorkflowId(), RunID: ex.GetRunId(), WorkflowType: d.GetType().GetName(), Status: d.GetStatus().String(), StartTime: d.GetStartTime().AsTime().UTC().Format(time.RFC3339Nano)}
			if d.GetStatus() != enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING {
				x.Decision = "closed-no-termination"
			} else {
				var err error
				x.scope, x.HistoryEvents, x.Decision, err = inspect(ctx, namespace, ex, r, historyDir)
				if err != nil {
					return nil, fmt.Errorf("history inspection failed for %s; no complete inventory produced", key)
				}
				if x.SubscriptionID == "" || x.TenantID == "" || x.EnvironmentID == "" {
					return nil, fmt.Errorf("missing scoped input for %s; no complete inventory produced", key)
				}
				groups[x.TenantID+"/"+x.EnvironmentID+"/"+x.SubscriptionID]++
			}
			result.Counts[x.Decision]++
			result.Rows = append(result.Rows, x)
		}
		if len(list.GetNextPageToken()) == 0 {
			break
		}
		if string(page) == string(list.GetNextPageToken()) {
			return nil, fmt.Errorf("repeated visibility page token; no complete inventory produced")
		}
		page = list.GetNextPageToken()
	}
	result.Counts["listed"] = len(result.Rows)
	result.Counts["subscriptions"] = len(groups)
	for _, n := range groups {
		result.Counts["running"] += n
		result.Counts["surplus-over-one"] += n - 1
	}
	sort.Slice(result.Rows, func(i, j int) bool {
		a, b := result.Rows[i], result.Rows[j]
		return a.WorkflowID+"/"+a.RunID < b.WorkflowID+"/"+b.RunID
	})
	return result, nil
}

func inspect(ctx context.Context, namespace string, ex *commonpb.WorkflowExecution, r reader, historyDir string) (scope, int, string, error) {
	var input scope
	count, starts := 0, 0
	blocked, preactivity := false, true
	var page []byte
	var events []*historypb.HistoryEvent
	for {
		if err := pace(ctx); err != nil {
			return input, count, "", err
		}
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		h, err := r.GetWorkflowExecutionHistory(callCtx, &workflowservice.GetWorkflowExecutionHistoryRequest{Namespace: namespace, Execution: ex, MaximumPageSize: 1000, NextPageToken: page, HistoryEventFilterType: enumspb.HISTORY_EVENT_FILTER_TYPE_ALL_EVENT})
		cancel()
		if err != nil {
			return input, count, "", err
		}
		for _, e := range h.GetHistory().GetEvents() {
			if historyDir != "" {
				events = append(events, e)
			}
			if e.GetEventId() != int64(count+1) {
				return input, count, "", fmt.Errorf("non-contiguous history")
			}
			count++
			if a := e.GetWorkflowExecutionStartedEventAttributes(); a != nil {
				starts++
				if err := converter.GetDefaultDataConverter().FromPayloads(a.GetInput(), &input); err != nil {
					return input, count, "", err
				}
			}
			if e.GetWorkflowTaskFailedEventAttributes().GetCause() == enumspb.WORKFLOW_TASK_FAILED_CAUSE_BAD_SEARCH_ATTRIBUTES {
				blocked = true
			}
			preactivity = preactivity && beforeAcceptedCommands(e)
		}
		if len(h.GetNextPageToken()) == 0 {
			break
		}
		if string(page) == string(h.GetNextPageToken()) {
			return input, count, "", fmt.Errorf("repeated history page token")
		}
		page = h.GetNextPageToken()
	}
	if starts != 1 {
		return input, count, "", fmt.Errorf("incomplete history")
	}
	if historyDir != "" {
		if err := exportHistory(historyDir, namespace, ex, events); err != nil {
			return input, count, "", fmt.Errorf("private history export failed")
		}
	}
	if blocked && preactivity {
		return input, count, "terminate-preactivity", nil
	}
	return input, count, "manual-reconciliation", nil
}

func exportHistory(dir, namespace string, ex *commonpb.WorkflowExecution, events []*historypb.HistoryEvent) error {
	history, err := protojson.Marshal(&historypb.History{Events: events})
	if err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Namespace  string          `json:"namespace"`
		WorkflowID string          `json:"workflow_id"`
		RunID      string          `json:"run_id"`
		History    json.RawMessage `json:"history"`
	}{namespace, ex.GetWorkflowId(), ex.GetRunId(), history})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	key := sha256.Sum256([]byte(namespace + "/" + ex.GetWorkflowId() + "/" + ex.GetRunId()))
	return os.Rename(f.Name(), filepath.Join(dir, fmt.Sprintf("%x.json", key)))
}

// A committed WFT, activity, local-activity marker, signal, child or any other
// event disqualifies automatic preactivity classification. No billing safety is
// inferred from a failed task's most recent log line alone.
func beforeAcceptedCommands(e *historypb.HistoryEvent) bool {
	switch e.GetEventType() {
	case enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED,
		enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED,
		enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED,
		enumspb.EVENT_TYPE_WORKFLOW_TASK_FAILED,
		enumspb.EVENT_TYPE_WORKFLOW_TASK_TIMED_OUT:
		return true
	}
	return false
}

// Keep inspection load below ten explicit read RPCs/second, sequentially.
func pace(ctx context.Context) error {
	timer := time.NewTimer(100 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
