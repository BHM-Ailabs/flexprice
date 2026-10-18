package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/flexprice/flexprice/internal/config"
	"github.com/flexprice/flexprice/internal/domain/secret"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/security"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/stretchr/testify/require"
)

type apiKeyLookupRepository struct {
	secret.Repository
	result *secret.Secret
	err    error
}

func (r *apiKeyLookupRepository) GetAPIKeyByValue(context.Context, string) (*secret.Secret, error) {
	return r.result, r.err
}

func TestVerifyAPIKeyAvailability(t *testing.T) {
	encryption, err := security.NewEncryptionService(&config.Configuration{Secrets: config.SecretsConfig{EncryptionKey: "test-encryption-key-for-unit-tests-only"}}, nil)
	require.NoError(t, err)
	expired := time.Now().Add(-time.Hour)
	active := func() *secret.Secret {
		return &secret.Secret{Type: types.SecretTypePrivateKey, BaseModel: types.BaseModel{Status: types.StatusPublished}}
	}
	for _, tt := range []struct {
		name        string
		result      *secret.Secret
		err         error
		unavailable bool
		valid       bool
	}{
		{name: "unknown", err: ierr.ErrNotFound},
		{name: "database down", err: ierr.WithError(errors.New("connection refused")).Mark(ierr.ErrDatabase), unavailable: true},
		{name: "timeout", err: context.DeadlineExceeded, unavailable: true},
		{name: "unclassified repository failure", err: errors.New("pool closed"), unavailable: true},
		{name: "empty repository result", unavailable: true},
		{name: "valid", result: active(), valid: true},
		{name: "revoked", result: &secret.Secret{Type: types.SecretTypePrivateKey, BaseModel: types.BaseModel{Status: types.StatusArchived}}},
		{name: "expired", result: &secret.Secret{Type: types.SecretTypePrivateKey, ExpiresAt: &expired, BaseModel: types.BaseModel{Status: types.StatusPublished}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := &secretService{repo: &apiKeyLookupRepository{result: tt.result, err: tt.err}, encryptionService: encryption}
			key, err := svc.VerifyAPIKey(context.Background(), "synthetic-key")
			if tt.valid {
				require.NoError(t, err)
				require.NotNil(t, key)
				return
			}
			require.Error(t, err)
			require.Equal(t, tt.unavailable, ierr.IsServiceUnavailable(err))
			require.Equal(t, !tt.unavailable, ierr.IsValidation(err))
			if tt.err != nil && tt.unavailable {
				require.ErrorIs(t, err, tt.err)
			}
		})
	}
}
