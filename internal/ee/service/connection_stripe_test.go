package service

import (
	"testing"

	"github.com/flexprice/flexprice/internal/types"
	"github.com/stretchr/testify/require"
)

type stripeRotationEncryption struct{}

func (stripeRotationEncryption) Encrypt(value string) (string, error) {
	return "encrypted:" + value, nil
}
func (stripeRotationEncryption) Decrypt(value string) (string, error) { return value, nil }
func (stripeRotationEncryption) Hash(value string) string             { return value }

func TestValidateStripeConnectionForCreate(t *testing.T) {
	tests := []struct {
		name     string
		metadata types.ConnectionMetadata
		wantErr  bool
	}{
		{name: "missing metadata", wantErr: true},
		{
			name: "missing webhook secret",
			metadata: types.ConnectionMetadata{Stripe: &types.StripeConnectionMetadata{
				PublishableKey: "test-publishable",
				SecretKey:      "test-server-secret",
			}},
			wantErr: true,
		},
		{
			name: "complete metadata",
			metadata: types.ConnectionMetadata{Stripe: &types.StripeConnectionMetadata{
				PublishableKey: "test-publishable",
				SecretKey:      "test-server-secret",
				WebhookSecret:  "test-webhook-secret",
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateStripeConnectionForCreate(types.SecretProviderStripe, tt.metadata)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
		})
	}
	require.NoError(t, validateStripeConnectionForCreate(types.SecretProviderPaystack, types.ConnectionMetadata{}))
}

func TestMergeStripeCredentialUpdatePreservesUnspecifiedCiphertext(t *testing.T) {
	svc := &connectionService{encryptionService: stripeRotationEncryption{}}
	existing := &types.StripeConnectionMetadata{
		PublishableKey: "encrypted:old-publishable",
		SecretKey:      "encrypted:old-secret",
		WebhookSecret:  "encrypted:old-webhook",
		AccountID:      "acct_old",
	}

	merged, err := svc.mergeStripeCredentialUpdate(existing, &types.StripeConnectionMetadata{
		SecretKey: "rotated-server-secret",
	})

	require.NoError(t, err)
	require.Equal(t, "encrypted:old-publishable", merged.PublishableKey)
	require.Equal(t, "encrypted:rotated-server-secret", merged.SecretKey)
	require.Equal(t, "encrypted:old-webhook", merged.WebhookSecret)
	require.Equal(t, "acct_old", merged.AccountID)
	require.Equal(t, "encrypted:old-secret", existing.SecretKey, "the persisted input must not be mutated before repository update")
}

func TestMergeStripeCredentialUpdateRequiresCompleteInitialCredentials(t *testing.T) {
	svc := &connectionService{encryptionService: stripeRotationEncryption{}}

	_, err := svc.mergeStripeCredentialUpdate(nil, &types.StripeConnectionMetadata{
		SecretKey: "only-one-secret",
	})

	require.Error(t, err)
}
