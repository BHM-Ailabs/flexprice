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
	ID        uint64         `json:"id"`
	Status    string         `json:"status"`
	Reference string         `json:"reference"`
	Amount    int64          `json:"amount"`
	Currency  string         `json:"currency"`
	PaidAt    string         `json:"paid_at"`
	Metadata  map[string]any `json:"metadata"`
}

type verifyTransactionResponse struct {
	Status  bool            `json:"status"`
	Message string          `json:"message"`
	Data    TransactionData `json:"data"`
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
