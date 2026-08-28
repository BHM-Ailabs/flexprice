package paystack

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/flexprice/flexprice/internal/domain/connection"
	ierr "github.com/flexprice/flexprice/internal/errors"
	"github.com/flexprice/flexprice/internal/httpclient"
	"github.com/flexprice/flexprice/internal/logger"
	"github.com/flexprice/flexprice/internal/security"
	"github.com/flexprice/flexprice/internal/types"
)

const paystackBaseURL = "https://api.paystack.co"

type Config struct {
	PublicKey string
	SecretKey string
}

type Client interface {
	GetConfig(ctx context.Context) (*Config, error)
	GetConnection(ctx context.Context) (*connection.Connection, error)
	HasConnection(ctx context.Context) bool
	InitializeTransaction(ctx context.Context, req InitializeTransactionRequest) (*InitializeTransactionData, error)
	VerifyTransaction(ctx context.Context, reference string) (*TransactionData, error)
	// ChargeAuthorization charges a reusable card authorization off-session.
	ChargeAuthorization(ctx context.Context, req ChargeAuthorizationRequest) (*TransactionData, error)
	VerifyWebhookSignature(ctx context.Context, payload []byte, signature string) error
}

type client struct {
	connectionRepo    connection.Repository
	encryptionService security.EncryptionService
	httpClient        httpclient.Client
	logger            *logger.Logger
	baseURL           string
}

func NewClient(
	connectionRepo connection.Repository,
	encryptionService security.EncryptionService,
	logger *logger.Logger,
) Client {
	return &client{
		connectionRepo:    connectionRepo,
		encryptionService: encryptionService,
		httpClient:        httpclient.NewDefaultClient(),
		logger:            logger,
		baseURL:           paystackBaseURL,
	}
}

func (c *client) GetConnection(ctx context.Context) (*connection.Connection, error) {
	return c.connectionRepo.GetByProvider(ctx, types.SecretProviderPaystack)
}

func (c *client) HasConnection(ctx context.Context) bool {
	conn, err := c.GetConnection(ctx)
	return err == nil && conn != nil && conn.Status == types.StatusPublished
}

func (c *client) GetConfig(ctx context.Context) (*Config, error) {
	conn, err := c.GetConnection(ctx)
	if err != nil {
		return nil, ierr.WithError(err).
			WithHint("Paystack connection is not configured for this environment").
			Mark(ierr.ErrNotFound)
	}
	if conn.EncryptedSecretData.Paystack == nil {
		return nil, ierr.NewError("paystack metadata is not configured").
			WithHint("Create a Paystack connection with a secret key").
			Mark(ierr.ErrValidation)
	}

	secretKey, err := c.encryptionService.Decrypt(conn.EncryptedSecretData.Paystack.SecretKey)
	if err != nil {
		return nil, ierr.WithError(err).
			WithHint("Unable to decrypt Paystack secret key").
			Mark(ierr.ErrInternal)
	}
	if secretKey == "" {
		return nil, ierr.NewError("paystack secret key is missing").
			WithHint("Configure the Paystack secret key on the connection").
			Mark(ierr.ErrValidation)
	}

	publicKey := ""
	if conn.EncryptedSecretData.Paystack.PublicKey != "" {
		publicKey, err = c.encryptionService.Decrypt(conn.EncryptedSecretData.Paystack.PublicKey)
		if err != nil {
			return nil, ierr.WithError(err).
				WithHint("Unable to decrypt Paystack public key").
				Mark(ierr.ErrInternal)
		}
	}

	return &Config{PublicKey: publicKey, SecretKey: secretKey}, nil
}

func (c *client) InitializeTransaction(ctx context.Context, req InitializeTransactionRequest) (*InitializeTransactionData, error) {
	var response initializeTransactionResponse
	if err := c.send(ctx, http.MethodPost, "/transaction/initialize", req, &response); err != nil {
		return nil, err
	}
	if !response.Status || response.Data.AuthorizationURL == "" || response.Data.Reference == "" {
		return nil, ierr.NewError("Paystack returned an invalid initialization response").
			WithHint("Paystack did not return a checkout URL and reference").
			Mark(ierr.ErrHTTPClient)
	}
	return &response.Data, nil
}

func (c *client) VerifyTransaction(ctx context.Context, reference string) (*TransactionData, error) {
	if reference == "" {
		return nil, ierr.NewError("paystack reference is required").Mark(ierr.ErrValidation)
	}
	var response verifyTransactionResponse
	endpoint := "/transaction/verify/" + reference
	if err := c.send(ctx, http.MethodGet, endpoint, nil, &response); err != nil {
		return nil, err
	}
	if !response.Status || response.Data.Reference == "" {
		return nil, ierr.NewError("Paystack returned an invalid verification response").
			WithHint("Paystack did not verify the transaction").
			Mark(ierr.ErrHTTPClient)
	}
	return &response.Data, nil
}

func (c *client) ChargeAuthorization(ctx context.Context, req ChargeAuthorizationRequest) (*TransactionData, error) {
	if req.AuthorizationCode == "" {
		return nil, ierr.NewError("paystack authorization code is required").
			WithHint("Charge a saved Paystack card only after an authorization was captured").
			Mark(ierr.ErrValidation)
	}
	if req.Email == "" {
		return nil, ierr.NewError("paystack customer email is required").
			WithHint("Paystack requires the authorization owner's email to charge it").
			Mark(ierr.ErrValidation)
	}
	if req.Amount <= 0 {
		return nil, ierr.NewError("paystack charge amount must be greater than zero").Mark(ierr.ErrValidation)
	}

	var response chargeAuthorizationResponse
	if err := c.send(ctx, http.MethodPost, "/transaction/charge_authorization", req, &response); err != nil {
		return nil, err
	}
	if !response.Status || response.Data.Status == "" {
		return nil, ierr.NewError("Paystack returned an invalid charge response").
			WithHint("Paystack did not accept the saved card charge").
			WithReportableDetails(map[string]any{"paystack_message": response.Message, "reference": req.Reference}).
			Mark(ierr.ErrHTTPClient)
	}
	return &response.Data, nil
}

func (c *client) VerifyWebhookSignature(ctx context.Context, payload []byte, signature string) error {
	config, err := c.GetConfig(ctx)
	if err != nil {
		return err
	}
	if !validWebhookSignature(payload, signature, config.SecretKey) {
		return ierr.NewError("invalid Paystack webhook signature").
			WithHint("Verify the x-paystack-signature header").
			Mark(ierr.ErrValidation)
	}
	return nil
}

func validWebhookSignature(payload []byte, signature, secret string) bool {
	if signature == "" || secret == "" {
		return false
	}
	provided, err := hex.DecodeString(strings.TrimSpace(signature))
	if err != nil {
		return false
	}
	mac := hmac.New(sha512.New, []byte(secret))
	_, _ = mac.Write(payload)
	return hmac.Equal(provided, mac.Sum(nil))
}

func (c *client) send(ctx context.Context, method, endpoint string, body any, out any) error {
	config, err := c.GetConfig(ctx)
	if err != nil {
		return err
	}

	var encoded []byte
	if body != nil {
		encoded, err = json.Marshal(body)
		if err != nil {
			return ierr.WithError(err).WithHint("Invalid Paystack request").Mark(ierr.ErrValidation)
		}
	}

	resp, err := c.httpClient.Send(ctx, &httpclient.Request{
		Method: method,
		URL:    c.baseURL + endpoint,
		Headers: map[string]string{
			"Authorization": "Bearer " + config.SecretKey,
			"Content-Type":  "application/json",
			"Accept":        "application/json",
		},
		Body: encoded,
	})
	if err != nil {
		c.logger.Error(ctx, "Paystack API request failed", "error", err, "method", method, "endpoint", endpoint)
		return ierr.WithError(err).
			WithHint("Unable to complete the Paystack request").
			WithReportableDetails(map[string]any{"method": method, "endpoint": endpoint}).
			Mark(ierr.ErrHTTPClient)
	}
	if out != nil {
		if err := json.Unmarshal(resp.Body, out); err != nil {
			return ierr.WithError(err).
				WithHint(fmt.Sprintf("Invalid response from Paystack for %s", endpoint)).
				Mark(ierr.ErrHTTPClient)
		}
	}
	return nil
}
