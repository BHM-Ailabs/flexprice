package main

import (
	"context"
	"errors"
	"testing"

	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	workflowpb "go.temporal.io/api/workflow/v1"
	"go.temporal.io/api/workflowservice/v1"
	"go.temporal.io/sdk/converter"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeReader struct {
	lists, histories int
	secondHistory    []*historypb.HistoryEvent
	historyError     bool
}

func (f *fakeReader) ListWorkflowExecutions(_ context.Context, req *workflowservice.ListWorkflowExecutionsRequest, _ ...grpc.CallOption) (*workflowservice.ListWorkflowExecutionsResponse, error) {
	f.lists++
	if req.Query != query || req.Namespace != "default" {
		panic("wrong read scope")
	}
	info := &workflowpb.WorkflowExecutionInfo{Execution: &commonpb.WorkflowExecution{WorkflowId: "wf", RunId: "run"}}
	if len(req.NextPageToken) == 0 {
		return &workflowservice.ListWorkflowExecutionsResponse{Executions: []*workflowpb.WorkflowExecutionInfo{info}, NextPageToken: []byte("next")}, nil
	}
	// Deliberately overlapping visibility pages must not duplicate a termination.
	return &workflowservice.ListWorkflowExecutionsResponse{Executions: []*workflowpb.WorkflowExecutionInfo{info}}, nil
}

func (f *fakeReader) DescribeWorkflowExecution(context.Context, *workflowservice.DescribeWorkflowExecutionRequest, ...grpc.CallOption) (*workflowservice.DescribeWorkflowExecutionResponse, error) {
	return &workflowservice.DescribeWorkflowExecutionResponse{WorkflowExecutionInfo: &workflowpb.WorkflowExecutionInfo{Status: enumspb.WORKFLOW_EXECUTION_STATUS_RUNNING, Type: &commonpb.WorkflowType{Name: "ProcessSubscriptionBillingWorkflow"}, StartTime: timestamppb.Now()}}, nil
}

func (f *fakeReader) GetWorkflowExecutionHistory(_ context.Context, req *workflowservice.GetWorkflowExecutionHistoryRequest, _ ...grpc.CallOption) (*workflowservice.GetWorkflowExecutionHistoryResponse, error) {
	f.histories++
	if f.historyError {
		return nil, errors.New("denied")
	}
	if len(req.NextPageToken) == 0 {
		input, _ := converter.GetDefaultDataConverter().ToPayloads(scope{SubscriptionID: "subs_123", TenantID: "tenant_123", EnvironmentID: "env_123"})
		return &workflowservice.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: []*historypb.HistoryEvent{{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{Input: input}}}}}, NextPageToken: []byte("next-history")}, nil
	}
	events := []*historypb.HistoryEvent{{EventId: 2, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_FAILED, Attributes: &historypb.HistoryEvent_WorkflowTaskFailedEventAttributes{WorkflowTaskFailedEventAttributes: &historypb.WorkflowTaskFailedEventAttributes{Cause: enumspb.WORKFLOW_TASK_FAILED_CAUSE_BAD_SEARCH_ATTRIBUTES}}}}
	for i, e := range f.secondHistory {
		e.EventId = int64(i + 3)
	}
	events = append(events, f.secondHistory...)
	return &workflowservice.GetWorkflowExecutionHistoryResponse{History: &historypb.History{Events: events}}, nil
}

func TestInventoryPaginatesAndRetainsNoCanonical(t *testing.T) {
	f := &fakeReader{}
	x, err := collect(context.Background(), "default", f)
	if err != nil {
		t.Fatal(err)
	}
	if f.lists != 2 || f.histories != 2 || len(x.Rows) != 1 || x.Counts["terminate-preactivity"] != 1 || x.Rows[0].CanonicalRun != "" || x.Rows[0].SubscriptionID != "subs_123" {
		t.Fatalf("unexpected inventory: %+v", x)
	}
}

func TestInventoryRequiresManualReconciliationForAnyAcceptedCommands(t *testing.T) {
	for _, event := range []enumspb.EventType{enumspb.EVENT_TYPE_ACTIVITY_TASK_SCHEDULED, enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED, enumspb.EVENT_TYPE_MARKER_RECORDED, enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_SIGNALED} {
		f := &fakeReader{secondHistory: []*historypb.HistoryEvent{{EventType: event}}}
		x, err := collect(context.Background(), "default", f)
		if err != nil || x.Counts["manual-reconciliation"] != 1 {
			t.Fatalf("event %v bypassed reconciliation: %v %+v", event, err, x)
		}
	}
}

func TestInventoryFailsClosedOnUnreadableHistory(t *testing.T) {
	x, err := collect(context.Background(), "default", &fakeReader{historyError: true})
	if err == nil || x != nil {
		t.Fatal("must not produce a partial termination manifest")
	}
}
