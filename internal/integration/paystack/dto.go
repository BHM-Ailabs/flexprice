package paystack

import "github.com/shopspring/decimal"

type InitializeTransactionRequest struct {
	Email       string         `json:"email"`
	Amount      int64          `json:"amount"`
	Currency    string         `json:"currency"`
	Reference   string         `json:"reference"`
	CallbackURL string         `json:"callback_url,omitempty"`
	Metadata    map[string]any `json:"metadata,omitempty"`
}

type InitializeTransactionData struct {
	AuthorizationURL string `json:"authorization_url"`
	AccessCode       string `json:"access_code"`
	Reference        string `json:"reference"`
}

type initializeTransactionResponse struct {
	Status  bool                      `json:"status"`
	Message string                    `json:"message"`
	Data    InitializeTransactionData `json:"data"`
}

type TransactionData struct {
	ID            uint64                    `json:"id"`
	Status        string                    `json:"status"`
	Reference     string                    `json:"reference"`
	Amount        int64                     `json:"amount"`
	Currency      string                    `json:"currency"`
	PaidAt        string                    `json:"paid_at"`
	Metadata      map[string]any            `json:"metadata"`
	Authorization *TransactionAuthorization `json:"authorization,omitempty"`
	Customer      *TransactionCustomer      `json:"customer,omitempty"`
}

// TransactionAuthorization is the card authorization Paystack returns on a verified
// transaction. A reusable authorization code can be charged again off-session via
// POST /transaction/charge_authorization.
type TransactionAuthorization struct {
	AuthorizationCode string `json:"authorization_code"`
	Reusable          bool   `json:"reusable"`
	Last4             string `json:"last4"`
	CardType          string `json:"card_type"`
	ExpMonth          string `json:"exp_month"`
	ExpYear           string `json:"exp_year"`
	Bank              string `json:"bank"`
}

// TransactionCustomer is the Paystack customer the transaction (and therefore the
// authorization) belongs to. Its email is required to charge the authorization again.
type TransactionCustomer struct {
	ID           uint64 `json:"id"`
	Email        string `json:"email"`
	CustomerCode string `json:"customer_code"`
}

type verifyTransactionResponse struct {
	Status  bool            `json:"status"`
	Message string          `json:"message"`
	Data    TransactionData `json:"data"`
}

// ChargeAuthorizationRequest is the POST /transaction/charge_authorization payload.
// Amount is in the currency's smallest unit (kobo for NGN, cents for USD).
type ChargeAuthorizationRequest struct {
	Email             string         `json:"email"`
	Amount            int64          `json:"amount"`
	Currency          string         `json:"currency"`
	AuthorizationCode string         `json:"authorization_code"`
	Reference         string         `json:"reference,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty"`
}

type chargeAuthorizationResponse struct {
	Status  bool            `json:"status"`
	Message string          `json:"message"`
	Data    TransactionData `json:"data"`
}

// ChargeAuthorizationParams charges an invoice against a saved Paystack authorization.
type ChargeAuthorizationParams struct {
	InvoiceID         string
	CustomerID        string
	PaymentID         string
	Amount            decimal.Decimal
	Currency          string
	AuthorizationCode string
	// Email is the Paystack customer email the authorization belongs to. When empty the
	// FlexPrice customer's email is used.
	Email    string
	Metadata map[string]string
}

// ChargeAuthorizationResult is the outcome of a successful off-session charge.
type ChargeAuthorizationResult struct {
	Reference        string
	GatewayPaymentID string
	Status           string
	Amount           decimal.Decimal
	Currency         string
}

type CreatePaymentLinkRequest struct {
	InvoiceID  string
	CustomerID string
	Amount     decimal.Decimal
	Currency   string
	SuccessURL string
	CancelURL  string
	Metadata   map[string]string
	PaymentID  string
}

type PaymentLinkResponse struct {
	Reference  string
	PaymentURL string
	AccessCode string
	Amount     decimal.Decimal
	Currency   string
	PaymentID  string
}

type WebhookEvent struct {
	Event string          `json:"event"`
	Data  TransactionData `json:"data"`
}
