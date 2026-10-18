package searchattr

import (
	"context"
	"fmt"
	"strings"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	"google.golang.org/grpc"
)

// AttributeLister deliberately exposes only the read-only operator RPC. Workers
// validate their namespace contract; registration remains an operator action.
type AttributeLister interface {
	ListSearchAttributes(context.Context, *operatorservice.ListSearchAttributesRequest, ...grpc.CallOption) (*operatorservice.ListSearchAttributesResponse, error)
}

// ValidateRegistration checks the namespace before workers poll or schedules are
// changed. UpsertSearchAttributes only queues a command: a nil SDK return cannot
// detect a later server-side BadSearchAttributes rejection of the workflow task.
func ValidateRegistration(ctx context.Context, namespace string, lister AttributeLister) error {
	if namespace == "" {
		return fmt.Errorf("Temporal search attribute validation requires an explicit namespace")
	}
	response, err := lister.ListSearchAttributes(ctx, &operatorservice.ListSearchAttributesRequest{Namespace: namespace})
	if err != nil {
		return fmt.Errorf("list Temporal search attributes in namespace %q: %w", namespace, err)
	}

	// All values are strings and the query helpers use exact equality. In
	// particular, Text would tokenize FailureReason and change those semantics.
	required := []string{
		SearchAttributeEnvironmentID,
		SearchAttributeFailingActivity,
		SearchAttributeFailureReason,
		SearchAttributeSubscriptionID,
		SearchAttributeTenantID,
	}
	var problems []string
	for _, name := range required {
		registered, ok := response.GetCustomAttributes()[name]
		if !ok {
			problems = append(problems, name+" is missing (requires Keyword)")
		} else if registered != enumspb.INDEXED_VALUE_TYPE_KEYWORD {
			problems = append(problems, fmt.Sprintf("%s has type %s (requires Keyword)", name, registered))
		}
	}
	if len(problems) != 0 {
		return fmt.Errorf("Temporal namespace %q search attribute contract is invalid: %s; an operator must inspect and register missing attributes before starting workers (see docs/temporal-search-attributes.md)", namespace, strings.Join(problems, "; "))
	}
	return nil
}
