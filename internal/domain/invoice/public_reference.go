package invoice

import "context"

// PublicReference is a native-owned presentation identifier. It never replaces provider IDs.
type PublicReference struct {
	Number         string   `json:"public_reference"`
	ReservationKey string   `json:"reservation_key"`
	InvoiceID      *string  `json:"invoice_id"`
	CustomerID     *string  `json:"customer_id"`
	Aliases        []string `json:"aliases"`
}
type ReferenceRequest struct {
	ReservationKey     string   `json:"reservation_key"`
	ExpectedCustomerID string   `json:"expected_customer_id,omitempty"`
	Aliases            []string `json:"aliases,omitempty"`
}

// ReferenceRepository is additive so unrelated repository implementations remain unchanged.
type ReferenceRepository interface {
	ReservePublicReference(context.Context, ReferenceRequest) (*PublicReference, error)
	BindPublicReference(context.Context, string, ReferenceRequest) (*PublicReference, error)
	BackfillPublicReferences(context.Context, string, int, bool) (map[string]interface{}, error)
}
