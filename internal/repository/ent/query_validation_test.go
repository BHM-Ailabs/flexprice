package ent

import (
	"testing"

	"github.com/flexprice/flexprice/internal/dsl"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/stretchr/testify/require"
)

func TestQueryFieldValidationPreservesDomainStatus(t *testing.T) {
	// Every current DSL field resolver is a local column-name mapping. Exercise
	// both shared entry points, including callers that add generic DB context.
	resolvers := map[string]dsl.FieldResolver{
		"addon":                      AddonQueryOptions{}.GetFieldResolver,
		"addon association":          AddonAssociationQueryOptions{}.GetFieldResolver,
		"alert log":                  AlertLogQueryOptions{}.GetFieldResolver,
		"alert settings":             AlertSettingsQueryOptions{}.GetFieldResolver,
		"connection":                 ConnectionQueryOptions{}.GetFieldResolver,
		"coupon":                     CouponQueryOptions{}.GetFieldResolver,
		"coupon application":         CouponApplicationQueryOptions{}.GetFieldResolver,
		"coupon association":         CouponAssociationQueryOptions{}.GetFieldResolver,
		"credit grant application":   CreditGrantApplicationQueryOptions{}.GetFieldResolver,
		"customer":                   CustomerQueryOptions{}.GetFieldResolver,
		"entity integration mapping": EntityIntegrationMappingQueryOptions{}.GetFieldResolver,
		"entitlement":                EntitlementQueryOptions{}.GetFieldResolver,
		"feature":                    FeatureQueryOptions{}.GetFieldResolver,
		"group":                      GroupQueryOptions{}.GetFieldResolver,
		"invoice":                    InvoiceQueryOptions{}.GetFieldResolver,
		"invoice line item":          InvoiceLineItemQueryOptions{}.GetFieldResolver,
		"plan":                       PlanQueryOptions{}.GetFieldResolver,
		"price":                      PriceQueryOptions{}.GetFieldResolver,
		"price unit":                 PriceUnitQueryOptions{}.GetFieldResolver,
		"subscription":               SubscriptionQueryOptions{}.GetFieldResolver,
		"subscription line item":     SubscriptionLineItemQueryOptions{}.GetFieldResolver,
		"subscription phase":         SubscriptionPhaseQueryOptions{}.GetFieldResolver,
		"tax applied":                TaxAppliedQueryOptions{}.GetFieldResolver,
		"tax rate":                   TaxRateQueryOptions{}.GetFieldResolver,
		"usage record":               UsageRecordQueryOptions{}.GetFieldResolver,
		"wallet transaction":         WalletTransactionQueryOptions{}.GetFieldResolver,
		"workflow execution":         WorkflowExecutionQueryOptions{}.GetFieldResolver,
	}
	field := "not_an_entity_column"
	for name, resolve := range resolvers {
		t.Run(name, func(t *testing.T) {
			_, filterErr := dsl.BuildPredicates([]*types.FilterCondition{{Field: &field}}, resolve)
			_, sortErr := dsl.BuildOrders([]*types.SortCondition{{Field: field}}, resolve)
			for _, err := range []error{filterErr, sortErr} {
				require.Error(t, err)
				require.True(t, ierr.IsValidation(err))
				status, _ := ierr.ResolveError(ierr.WithError(err).Mark(ierr.ErrDatabase))
				require.Equal(t, 400, status)
			}
		})
	}
}
