package service

import (
	"context"
	"testing"

	"github.com/flexprice/flexprice/internal/api/dto"
	authprovider "github.com/flexprice/flexprice/internal/auth"
	"github.com/flexprice/flexprice/internal/config"
	domainauth "github.com/flexprice/flexprice/internal/domain/auth"
	"github.com/flexprice/flexprice/internal/domain/user"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/types"
	"github.com/stretchr/testify/require"
)

type loginUserRepository struct{ user.Repository }

func (*loginUserRepository) GetByEmail(context.Context, string) (*user.User, error) {
	return &user.User{ID: "synthetic", BaseModel: types.BaseModel{TenantID: "synthetic-tenant"}}, nil
}

type unavailableAuthProvider struct{ authprovider.Provider }

func (*unavailableAuthProvider) GetProvider() types.AuthProvider { return types.AuthProviderSupabase }
func (*unavailableAuthProvider) Login(context.Context, authprovider.AuthRequest, *domainauth.Auth) (*authprovider.AuthResponse, error) {
	return nil, ierr.ErrServiceUnavailable
}

func TestLoginPreservesProviderOutage(t *testing.T) {
	svc := &authService{ServiceParams: ServiceParams{Config: &config.Configuration{}, UserRepo: &loginUserRepository{}}, authProvider: &unavailableAuthProvider{}}
	_, err := svc.Login(context.Background(), &dto.LoginRequest{Email: "synthetic@example.com", Password: "synthetic"})
	require.Error(t, err)
	require.True(t, ierr.IsServiceUnavailable(err))
	require.False(t, ierr.IsPermissionDenied(err))
}
