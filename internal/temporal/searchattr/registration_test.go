package searchattr

import (
	"context"
	"errors"
	"strings"
	"testing"

	enumspb "go.temporal.io/api/enums/v1"
	"go.temporal.io/api/operatorservice/v1"
	"google.golang.org/grpc"
)

type attributeListerFunc func(context.Context, *operatorservice.ListSearchAttributesRequest, ...grpc.CallOption) (*operatorservice.ListSearchAttributesResponse, error)

func (f attributeListerFunc) ListSearchAttributes(ctx context.Context, req *operatorservice.ListSearchAttributesRequest, opts ...grpc.CallOption) (*operatorservice.ListSearchAttributesResponse, error) {
	return f(ctx, req, opts...)
}

func TestValidateRegistration(t *testing.T) {
	complete := func() map[string]enumspb.IndexedValueType {
		return map[string]enumspb.IndexedValueType{
			SearchAttributeEnvironmentID:   enumspb.INDEXED_VALUE_TYPE_KEYWORD,
			SearchAttributeFailingActivity: enumspb.INDEXED_VALUE_TYPE_KEYWORD,
			SearchAttributeFailureReason:   enumspb.INDEXED_VALUE_TYPE_KEYWORD,
			SearchAttributeSubscriptionID:  enumspb.INDEXED_VALUE_TYPE_KEYWORD,
			SearchAttributeTenantID:        enumspb.INDEXED_VALUE_TYPE_KEYWORD,
		}
	}
	for _, tc := range []struct {
		name       string
		attributes map[string]enumspb.IndexedValueType
		want       []string
	}{
		{name: "registered namespace", attributes: complete()},
		{name: "missing namespace aliases", want: []string{"EnvironmentID is missing", "FailingActivity is missing", "FailureReason is missing", "SubscriptionID is missing", "TenantID is missing"}},
		{name: "type mismatch", attributes: func() map[string]enumspb.IndexedValueType {
			m := complete()
			m[SearchAttributeTenantID] = enumspb.INDEXED_VALUE_TYPE_INT
			return m
		}(), want: []string{"TenantID has type Int"}},
		{name: "text changes exact match semantics", attributes: func() map[string]enumspb.IndexedValueType {
			m := complete()
			m[SearchAttributeFailureReason] = enumspb.INDEXED_VALUE_TYPE_TEXT
			return m
		}(), want: []string{"FailureReason has type Text"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			ctx := context.WithValue(context.Background(), struct{}{}, "test context")
			err := ValidateRegistration(ctx, "billing-production", attributeListerFunc(func(got context.Context, req *operatorservice.ListSearchAttributesRequest, _ ...grpc.CallOption) (*operatorservice.ListSearchAttributesResponse, error) {
				calls++
				if got != ctx || req.Namespace != "billing-production" {
					t.Fatal("did not preserve the configured namespace and context")
				}
				return &operatorservice.ListSearchAttributesResponse{CustomAttributes: tc.attributes}, nil
			}))
			if calls != 1 {
				t.Fatalf("got %d list calls, want 1", calls)
			}
			if len(tc.want) == 0 {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil {
				t.Fatal("missing or invalid mappings must block startup")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("%q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestValidateRegistrationRPCFailure(t *testing.T) {
	want := errors.New("operator RPC denied")
	err := ValidateRegistration(context.Background(), "default", attributeListerFunc(func(context.Context, *operatorservice.ListSearchAttributesRequest, ...grpc.CallOption) (*operatorservice.ListSearchAttributesResponse, error) {
		return nil, want
	}))
	if !errors.Is(err, want) {
		t.Fatalf("got %v, want wrapped RPC error", err)
	}
}

func TestValidateRegistrationRequiresNamespace(t *testing.T) {
	err := ValidateRegistration(context.Background(), "", attributeListerFunc(func(context.Context, *operatorservice.ListSearchAttributesRequest, ...grpc.CallOption) (*operatorservice.ListSearchAttributesResponse, error) {
		t.Fatal("must not query an implicit namespace")
		return nil, nil
	}))
	if err == nil {
		t.Fatal("expected an explicit namespace requirement")
	}
}
