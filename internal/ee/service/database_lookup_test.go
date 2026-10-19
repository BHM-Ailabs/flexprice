package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flexprice/flexprice/ent"
	"github.com/flexprice/flexprice/internal/api/dto"
	"github.com/flexprice/flexprice/internal/domain/addon"
	"github.com/flexprice/flexprice/internal/domain/costsheet"
	couponassociation "github.com/flexprice/flexprice/internal/domain/coupon_association"
	"github.com/flexprice/flexprice/internal/domain/entitlement"
	"github.com/flexprice/flexprice/internal/domain/feature"
	"github.com/flexprice/flexprice/internal/domain/plan"
	"github.com/flexprice/flexprice/internal/domain/subscription"
	"github.com/flexprice/flexprice/internal/domain/taxassociation"
	"github.com/flexprice/flexprice/internal/domain/wallet"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

type lookupPlanRepository struct {
	plan.Repository
	err error
}

func (r lookupPlanRepository) Get(context.Context, string) (*plan.Plan, error) {
	return nil, r.err
}

type lookupAddonRepository struct {
	addon.Repository
	err error
}

func (r lookupAddonRepository) GetByID(context.Context, string) (*addon.Addon, error) {
	return nil, r.err
}

type lookupSubscriptionRepository struct {
	subscription.Repository
	err error
}

func (r lookupSubscriptionRepository) Get(context.Context, string) (*subscription.Subscription, error) {
	return nil, r.err
}

type lookupCostsheetRepository struct {
	costsheet.Repository
	err error
}

func (r lookupCostsheetRepository) GetByID(context.Context, string) (*costsheet.Costsheet, error) {
	return nil, r.err
}

type lookupFeatureRepository struct {
	feature.Repository
	err error
}

func (r lookupFeatureRepository) Get(context.Context, string) (*feature.Feature, error) {
	return nil, r.err
}

type lookupWalletRepository struct {
	wallet.Repository
	err error
}

func (r lookupWalletRepository) GetWalletByID(context.Context, string) (*wallet.Wallet, error) {
	return nil, r.err
}

func (r lookupWalletRepository) GetTransactionByID(context.Context, string) (*wallet.Transaction, error) {
	return nil, r.err
}

type lookupCouponAssociationRepository struct {
	couponassociation.Repository
	err error
}

func (r lookupCouponAssociationRepository) Get(context.Context, string) (*couponassociation.CouponAssociation, error) {
	return nil, r.err
}

type lookupTaxAssociationRepository struct {
	taxassociation.Repository
	err error
}

func (r lookupTaxAssociationRepository) Get(context.Context, string) (*taxassociation.TaxAssociation, error) {
	return nil, r.err
}

type lookupEntitlementRepository struct {
	entitlement.Repository
	err error
}

func (r lookupEntitlementRepository) ListByPlanIDs(context.Context, []string) ([]*entitlement.Entitlement, error) {
	return nil, nil
}

func (r lookupEntitlementRepository) Get(context.Context, string) (*entitlement.Entitlement, error) {
	return nil, r.err
}

// These paths previously discarded the repository cause and constructed a new
// not-found error, which a classifier alone cannot correct.
func TestRepositoryLookupFailuresPreserved(t *testing.T) {
	ctx := context.Background()
	for _, tt := range []struct {
		name   string
		err    error
		status int
	}{
		{"disk unavailable", ierr.WithError(&pq.Error{Code: "53100"}).Mark(ierr.ErrDatabase), 503},
		{"unclassified database error", ierr.WithError(errors.New("synthetic unknown failure")).Mark(ierr.ErrDatabase), 500},
		{"genuine not found", ierr.WithError(&ent.NotFoundError{}).Mark(ierr.ErrNotFound), 404},
	} {
		t.Run(tt.name, func(t *testing.T) {
			params := ServiceParams{
				Logger:                &logger.Logger{SugaredLogger: zap.NewNop().Sugar()},
				PlanRepo:              lookupPlanRepository{err: tt.err},
				AddonRepo:             lookupAddonRepository{err: tt.err},
				SubRepo:               lookupSubscriptionRepository{err: tt.err},
				CostSheetRepo:         lookupCostsheetRepository{err: tt.err},
				FeatureRepo:           lookupFeatureRepository{err: tt.err},
				WalletRepo:            lookupWalletRepository{err: tt.err},
				CouponAssociationRepo: lookupCouponAssociationRepository{err: tt.err},
				TaxAssociationRepo:    lookupTaxAssociationRepository{err: tt.err},
				EntitlementRepo:       lookupEntitlementRepository{err: tt.err},
			}
			operations := map[string]func() error{
				"delete feature": func() error { return (&featureService{ServiceParams: params}).DeleteFeature(ctx, "feature") },
				"top up wallet": func() error {
					_, err := (&walletService{ServiceParams: params}).TopUpWallet(ctx, "wallet", &dto.TopUpWalletRequest{})
					return err
				},
				"complete wallet purchase": func() error {
					return (&walletService{ServiceParams: params}).completePurchasedCreditTransaction(ctx, "transaction")
				},
				"subscription entitlement override": func() error {
					return (&subscriptionService{ServiceParams: params}).ProcessSubscriptionEntitlementOverrides(ctx,
						&subscription.Subscription{ID: "subscription", PlanID: "plan"},
						[]dto.OverrideEntitlementRequest{{EntitlementID: "entitlement"}})
				},
				"preview remove coupon": func() error {
					_, err := (&subscriptionModificationService{serviceParams: params}).previewRemoveCoupon(ctx, "subscription", "association", time.Time{})
					return err
				},
				"preview remove tax": func() error {
					_, err := (&subscriptionModificationService{serviceParams: params}).previewRemoveTax(ctx, "subscription", "association", time.Time{})
					return err
				},
			}
			for _, entityType := range []types.PriceEntityType{types.PRICE_ENTITY_TYPE_PLAN, types.PRICE_ENTITY_TYPE_ADDON, types.PRICE_ENTITY_TYPE_SUBSCRIPTION, types.PRICE_ENTITY_TYPE_COSTSHEET} {
				operations["price entity "+string(entityType)] = func() error {
					return (&priceService{ServiceParams: params}).validateEntityExists(ctx, entityType, "entity")
				}
			}
			for _, entityType := range []types.EntitlementEntityType{types.ENTITLEMENT_ENTITY_TYPE_PLAN, types.ENTITLEMENT_ENTITY_TYPE_ADDON, types.ENTITLEMENT_ENTITY_TYPE_SUBSCRIPTION} {
				operations["entitlement entity "+string(entityType)] = func() error {
					_, err := (&entitlementService{ServiceParams: params}).CreateEntitlement(ctx, dto.CreateEntitlementRequest{
						EntityType: entityType, EntityID: "entity", FeatureID: "feature", FeatureType: types.FeatureTypeBoolean,
					})
					return err
				}
			}
			for name, operation := range operations {
				t.Run(name, func(t *testing.T) {
					err := operation()
					require.Error(t, err)
					status, _ := ierr.ResolveError(err)
					require.Equal(t, tt.status, status)
					if tt.status != 404 {
						require.Same(t, tt.err, err, "preserve the cause and original classification")
						require.False(t, ierr.IsNotFound(err))
					}
				})
			}
		})
	}
}
