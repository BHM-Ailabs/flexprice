package searchattr

// This package queues custom search attribute commands for workflow visibility.
// The attributes must be registered in the workflow's namespace before workers
// start (ValidateRegistration checks this outside workflow execution).
//
// Panic recovery and warning logs handle local SDK errors only. A successful
// UpsertSearchAttributes call does not validate server registration: rejection
// happens when Temporal completes the workflow task and can prevent all commands
// in that task, including activity scheduling, from being committed.

import (
	"fmt"

	"go.temporal.io/sdk/workflow"
)

const (
	// Custom Search Attribute Keys
	// These must be registered in your Temporal namespace before use
	SearchAttributeFailingActivity = "FailingActivity"
	SearchAttributeFailureReason   = "FailureReason"
	SearchAttributeSubscriptionID  = "SubscriptionID"
	SearchAttributeTenantID        = "TenantID"
	SearchAttributeEnvironmentID   = "EnvironmentID"
)

// UpsertFailureSearchAttributes upserts search attributes when an activity fails
// This allows querying failed workflows by activity name and failure reason in Temporal UI/CLI
//
// Parameters:
//   - ctx: Workflow context
//   - activityName: Name of the failed activity (e.g., "CreateDraftInvoicesActivity")
//   - err: The error that occurred
//   - subscriptionID: Optional subscription ID for additional filtering
//
// Example usage in workflow:
//
//	err := workflow.ExecuteActivity(ctx, ActivityCreateDraftInvoices, input).Get(ctx, &output)
//	if err != nil {
//	    UpsertFailureSearchAttributes(ctx, ActivityCreateDraftInvoices, err, input.SubscriptionID)
//	    return nil, err
//	}
func UpsertFailureSearchAttributes(
	ctx workflow.Context,
	activityName string,
	err error,
	subscriptionID string,
) {
	// Fail-safe: Early returns to prevent any issues
	if ctx == nil || err == nil {
		return
	}

	// Wrap everything in a recover to catch any panics
	defer func() {
		if r := recover(); r != nil {
			logger := workflow.GetLogger(ctx)
			logger.Warn("Recovered from panic in UpsertFailureSearchAttributes",
				"panic", r,
				"activity", activityName)
		}
	}()

	logger := workflow.GetLogger(ctx)

	// Build search attributes map with safe string conversion
	searchAttrs := make(map[string]interface{})

	// Safely add activity name
	if activityName != "" {
		searchAttrs[SearchAttributeFailingActivity] = activityName
	}

	// Safely add failure reason
	errMsg := "unknown error"
	if err != nil {
		errMsg = err.Error()
	}
	// Temporal has a 2048 byte limit - truncate to 2000 bytes to be safe
	searchAttrs[SearchAttributeFailureReason] = truncateStringByBytes(errMsg, 2000)

	// Safely add subscription ID if provided
	if subscriptionID != "" {
		searchAttrs[SearchAttributeSubscriptionID] = subscriptionID
	}

	// Upsert the search attributes - if this fails, only log warning
	if upsertErr := workflow.UpsertSearchAttributes(ctx, searchAttrs); upsertErr != nil {
		// Don't fail the workflow, just log a warning
		logger.Warn("Failed to upsert failure search attributes (non-critical)",
			"activity", activityName,
			"upsert_error", upsertErr.Error(),
			"note", "Local SDK error; server-side validation occurs on workflow-task completion")
	} else {
		logger.Debug("Queued failure search attribute command",
			"activity", activityName,
			"failure_reason_preview", truncateString(errMsg, 100))
	}
}

// UpsertWorkflowSearchAttributes upserts general workflow search attributes
// Useful for setting searchable metadata at workflow start
//
// Local SDK failures are logged. Server-side command validation still requires
// the namespace contract to have been validated before worker startup.
//
// Example usage:
//
//	UpsertWorkflowSearchAttributes(ctx, map[string]interface{}{
//	    SearchAttributeSubscriptionID: input.SubscriptionID,
//	    SearchAttributeTenantID: input.TenantID,
//	    SearchAttributeEnvironmentID: input.EnvironmentID,
//	})
func UpsertWorkflowSearchAttributes(ctx workflow.Context, attrs map[string]interface{}) {
	// Fail-safe: Early return if context or attrs are invalid
	if ctx == nil || len(attrs) == 0 {
		return
	}

	// Wrap in recover to catch any panics
	defer func() {
		if r := recover(); r != nil {
			logger := workflow.GetLogger(ctx)
			logger.Warn("Recovered from panic in UpsertWorkflowSearchAttributes",
				"panic", r)
		}
	}()

	logger := workflow.GetLogger(ctx)

	// Attempt to upsert - if this fails, only log warning
	if err := workflow.UpsertSearchAttributes(ctx, attrs); err != nil {
		// Don't fail the workflow, just log a warning
		logger.Warn("Failed to upsert workflow search attributes (non-critical)",
			"error", err.Error(),
			"attribute_count", len(attrs),
			"note", "Local SDK error; server-side validation occurs on workflow-task completion")
	} else {
		logger.Debug("Queued workflow search attribute command", "count", len(attrs))
	}
}

// ClearFailureSearchAttributes clears failure-related search attributes
// Useful when retrying after a failure
//
// Server-side command validation requires registered namespace attributes.
func ClearFailureSearchAttributes(ctx workflow.Context) {
	// Fail-safe: Early return if context is invalid
	if ctx == nil {
		return
	}

	// Wrap in recover to catch any panics
	defer func() {
		if r := recover(); r != nil {
			logger := workflow.GetLogger(ctx)
			logger.Warn("Recovered from panic in ClearFailureSearchAttributes",
				"panic", r)
		}
	}()

	logger := workflow.GetLogger(ctx)

	// Set empty strings to clear the attributes
	searchAttrs := map[string]interface{}{
		SearchAttributeFailingActivity: "",
		SearchAttributeFailureReason:   "",
	}

	// Attempt to clear - if this fails, only log warning
	if err := workflow.UpsertSearchAttributes(ctx, searchAttrs); err != nil {
		// Don't fail the workflow, just log a warning
		logger.Warn("Failed to clear failure search attributes (non-critical)",
			"error", err.Error(),
			"note", "Local SDK error; server-side validation occurs on workflow-task completion")
	}
}

// truncateString truncates a string to the specified length (character count)
// FAIL-SAFE: Returns empty string on any error
func truncateString(s string, maxLen int) string {
	defer func() {
		if r := recover(); r != nil {
			// Silently recover from any panic
		}
	}()

	if s == "" || maxLen <= 0 {
		return ""
	}

	if len(s) <= maxLen {
		return s
	}

	// Safely truncate
	if maxLen > 3 {
		return s[:maxLen-3] + "..."
	}

	return s[:maxLen]
}

// truncateStringByBytes truncates a string to the specified byte limit
// This is important for Temporal search attributes which have byte limits, not character limits
// FAIL-SAFE: Returns empty string on any error
func truncateStringByBytes(s string, maxBytes int) string {
	defer func() {
		if r := recover(); r != nil {
			// Silently recover from any panic
		}
	}()

	if s == "" || maxBytes <= 0 {
		return ""
	}

	// If string is already within limit, return as-is
	if len(s) <= maxBytes {
		return s
	}

	// Truncate by bytes, ensuring we don't cut in the middle of a multi-byte character
	ellipsis := "..."
	maxBytes -= len(ellipsis) // Reserve space for ellipsis

	if maxBytes <= 0 {
		return ""
	}

	// Start from maxBytes and walk backward to find a valid UTF-8 boundary
	truncated := s
	for len(truncated) > maxBytes {
		// Remove last rune to ensure we don't cut in middle of multi-byte char
		truncated = truncated[:len(truncated)-1]
	}

	// Trim to the last complete UTF-8 character
	for len(truncated) > 0 && len(truncated) > maxBytes {
		// Move back one byte at a time until we're under the limit
		truncated = truncated[:len(truncated)-1]
	}

	return truncated + ellipsis
}

// GetSearchAttributeQueries returns example queries for filtering workflows
func GetSearchAttributeQueries() map[string]string {
	return map[string]string{
		"all_failed_workflows":      fmt.Sprintf("%s != ''", SearchAttributeFailingActivity),
		"specific_activity_failure": fmt.Sprintf("%s = 'CreateDraftInvoicesActivity'", SearchAttributeFailingActivity),
		"specific_error_message":    fmt.Sprintf("%s = 'quantity must be non-negative'", SearchAttributeFailureReason),
		"subscription_failures": fmt.Sprintf("%s != '' AND %s = 'subs_123'",
			SearchAttributeFailingActivity, SearchAttributeSubscriptionID),
		"tenant_failures": fmt.Sprintf("%s != '' AND %s = 'tenant_456'",
			SearchAttributeFailingActivity, SearchAttributeTenantID),
	}
}
