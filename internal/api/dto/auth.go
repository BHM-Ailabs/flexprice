package dto

import (
	"github.com/flexprice/flexprice/internal/validator"
)

type SignUpRequest struct {
	Email      string            `json:"email" binding:"required,email" validate:"email"`
	Password   string            `json:"password" binding:"omitempty,min=8" validate:"omitempty,min=8"`
	TenantName string            `json:"tenant_name" binding:"omitempty" validate:"omitempty"`
	Token      string            `json:"token" binding:"omitempty" validate:"omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty" binding:"omitempty" validate:"omitempty"`
}

type LoginRequest struct {
	Email    string `json:"email" binding:"required,email" validate:"email"`
	Password string `json:"password" binding:"required" validate:"min=8"`
	Token    string `json:"token" binding:"omitempty" validate:"omitempty"`
}

type AuthResponse struct {
	Token    string `json:"token"`
	UserID   string `json:"user_id"`
	TenantID string `json:"tenant_id"`
}

type PlaqadCallbackRequest struct {
	Code         string `json:"code" binding:"required"`
	CodeVerifier string `json:"code_verifier" binding:"required"`
}

type PlaqadCallbackResponse struct {
	Token     string          `json:"token"`
	ExpiresAt int64           `json:"expiresAt"`
	User      PlaqadTokenUser `json:"user"`
}

type PlaqadTokenUser struct {
	ID    string `json:"sub"`
	Email string `json:"email"`
	Name  string `json:"name"`
}

func (r *SignUpRequest) Validate() error {
	return validator.ValidateRequest(r)
}

func (r *LoginRequest) Validate() error {
	return validator.ValidateRequest(r)
}
