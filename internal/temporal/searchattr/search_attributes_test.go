package searchattr

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/mock"
	commonpb "go.temporal.io/api/common/v1"
	enumspb "go.temporal.io/api/enums/v1"
	historypb "go.temporal.io/api/history/v1"
	taskqueuepb "go.temporal.io/api/taskqueue/v1"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func TestBoundedKeyword(t *testing.T) {
	for _, input := range []string{
		"", "quantity must be non-negative", strings.Repeat("a", 255),
		strings.Repeat("a", 2000), strings.Repeat("界🙂", 300),
		strings.Repeat("<>&\n\x01", 600), "bad\xffUTF8\x00value",
	} {
		got := boundedKeyword(input)
		payload, err := json.Marshal(got)
		if err != nil || len(payload) > 2048 || !utf8.ValidString(got) || strings.ContainsRune(got, 0) || utf8.RuneCountInString(got) > 255 {
			t.Fatalf("unsafe Keyword: runes=%d JSON bytes=%d err=%v", utf8.RuneCountInString(got), len(payload), err)
		}
		if validKeywordID(input) && utf8.RuneCountInString(input) <= 255 && got != input {
			t.Fatal("changed a valid short Keyword")
		}
	}
}

// The SDK matches upsert commands by event type, not payload. Preserve an old
// accepted long-value history while writing bounded previews on new tasks.
func TestReplayHistoricalUnboundedSearchAttribute(t *testing.T) {
	oldPayload, err := converter.GetDefaultDataConverter().ToPayload(strings.Repeat("x", 2000))
	if err != nil {
		t.Fatal(err)
	}
	oldPayload.Metadata["type"] = []byte("Keyword")
	history := &historypb.History{Events: []*historypb.HistoryEvent{
		{EventId: 1, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_STARTED, Attributes: &historypb.HistoryEvent_WorkflowExecutionStartedEventAttributes{WorkflowExecutionStartedEventAttributes: &historypb.WorkflowExecutionStartedEventAttributes{WorkflowType: &commonpb.WorkflowType{Name: "historicalMetadataWorkflow"}, TaskQueue: &taskqueuepb.TaskQueue{Name: "test"}}}},
		{EventId: 2, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_SCHEDULED, Attributes: &historypb.HistoryEvent_WorkflowTaskScheduledEventAttributes{WorkflowTaskScheduledEventAttributes: &historypb.WorkflowTaskScheduledEventAttributes{TaskQueue: &taskqueuepb.TaskQueue{Name: "test"}}}},
		{EventId: 3, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_STARTED, Attributes: &historypb.HistoryEvent_WorkflowTaskStartedEventAttributes{WorkflowTaskStartedEventAttributes: &historypb.WorkflowTaskStartedEventAttributes{ScheduledEventId: 2}}},
		{EventId: 4, EventType: enumspb.EVENT_TYPE_WORKFLOW_TASK_COMPLETED, Attributes: &historypb.HistoryEvent_WorkflowTaskCompletedEventAttributes{WorkflowTaskCompletedEventAttributes: &historypb.WorkflowTaskCompletedEventAttributes{ScheduledEventId: 2, StartedEventId: 3}}},
		{EventId: 5, EventType: enumspb.EVENT_TYPE_UPSERT_WORKFLOW_SEARCH_ATTRIBUTES, Attributes: &historypb.HistoryEvent_UpsertWorkflowSearchAttributesEventAttributes{UpsertWorkflowSearchAttributesEventAttributes: &historypb.UpsertWorkflowSearchAttributesEventAttributes{WorkflowTaskCompletedEventId: 4, SearchAttributes: &commonpb.SearchAttributes{IndexedFields: map[string]*commonpb.Payload{SearchAttributeFailureReason: oldPayload}}}}},
		{EventId: 6, EventType: enumspb.EVENT_TYPE_WORKFLOW_EXECUTION_COMPLETED, Attributes: &historypb.HistoryEvent_WorkflowExecutionCompletedEventAttributes{WorkflowExecutionCompletedEventAttributes: &historypb.WorkflowExecutionCompletedEventAttributes{WorkflowTaskCompletedEventId: 4}}},
	}}
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(func(ctx workflow.Context) error {
		UpsertWorkflowSearchAttributes(ctx, map[string]interface{}{SearchAttributeFailureReason: strings.Repeat("x", 2000)})
		return nil
	}, workflow.RegisterOptions{Name: "historicalMetadataWorkflow"})
	if err := replayer.ReplayWorkflowHistory(nil, history); err != nil {
		t.Fatal(err)
	}
}

func TestKeywordIdentityDoesNotAliasTruncatedIDs(t *testing.T) {
	for _, input := range []string{strings.Repeat("a", 256), "tenant\x00bad", "tenant\xffbad"} {
		if keywordIdentity(input) != "" {
			t.Fatal("invalid identity must be cleared, never shortened into another identity")
		}
	}
	if keywordIdentity("tenant_123") != "tenant_123" {
		t.Fatal("valid identity changed")
	}
}

func TestWorkflowUpsertsBoundSQLKeywords(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	seen := 0
	env.OnUpsertSearchAttributes(mock.Anything).Run(func(args mock.Arguments) {
		attrs := args.Get(0).(map[string]interface{})
		seen++
		for _, value := range attrs {
			s := value.(string)
			payload, _ := json.Marshal(s)
			if !validKeywordID(s) || len(payload) > 2048 {
				t.Errorf("workflow queued an unsafe SQL Keyword")
			}
		}
	}).Return(nil)
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		UpsertWorkflowSearchAttributes(ctx, map[string]interface{}{
			SearchAttributeTenantID:      strings.Repeat("t", 256),
			SearchAttributeEnvironmentID: "env_123",
			SearchAttributeFailureReason: strings.Repeat("<🙂\x00", 1000),
		})
		UpsertFailureSearchAttributes(ctx, strings.Repeat("activity", 100), errors.New(strings.Repeat("&🙂\x00", 1000)), strings.Repeat("s", 256))
		return nil
	})
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	if seen != 2 {
		t.Fatalf("got %d upserts, want existing command count 2", seen)
	}
}
