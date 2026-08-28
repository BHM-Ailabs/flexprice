package paystack

// Outcome classification for off-session charges. A charge attempt has exactly three possible
// outcomes and the money path depends on telling them apart: proven collected, proven declined,
// or unknown. Never collapse "unknown" into "failed" — that is how customers get charged twice.

import (
	"errors"
	"fmt"
	"strings"

	"github.com/flexprice/flexprice/internal/types"
)

// terminalFailureStatuses are the Paystack transaction statuses that prove no money was taken.
// Anything else (pending, ongoing, queued, empty) leaves the outcome unknown.
var terminalFailureStatuses = []string{"failed", "abandoned", "reversed"}

func isTerminalFailureStatus(status string) bool {
	for _, candidate := range terminalFailureStatuses {
		if strings.EqualFold(status, candidate) {
			return true
		}
	}
	return false
}

// ChargeOutcomeUnknownError means the charge neither proved success nor proved failure: the card
// may already have been debited. Callers must not retry it, must not fall back to another payment
// method, and must leave the payment in a non-terminal state for the webhook or a reconciliation
// sweep to settle.
type ChargeOutcomeUnknownError struct {
	Reference string
	Status    string
	cause     error
}

func (e *ChargeOutcomeUnknownError) Error() string {
	return fmt.Sprintf("paystack charge outcome unknown (reference %s, status %q): %v", e.Reference, e.Status, e.cause)
}

func (e *ChargeOutcomeUnknownError) Unwrap() error { return e.cause }

// NewChargeOutcomeUnknownError builds an unknown-outcome error for a charge reference.
func NewChargeOutcomeUnknownError(reference, status string, cause error) error {
	if cause == nil {
		cause = errors.New("no verified transaction")
	}
	return &ChargeOutcomeUnknownError{Reference: reference, Status: status, cause: cause}
}

// IsChargeOutcomeUnknown reports whether err (or anything it wraps) is an unknown charge outcome.
func IsChargeOutcomeUnknown(err error) bool {
	var target *ChargeOutcomeUnknownError
	return errors.As(err, &target)
}

// ChargeCollectedError means Paystack definitely collected the money but FlexPrice failed to
// persist that locally. The charge must never be retried and no other payment method may be
// used for the same amount; the webhook or a reconciliation sweep persists the truth.
type ChargeCollectedError struct {
	Reference string
	cause     error
}

func (e *ChargeCollectedError) Error() string {
	return fmt.Sprintf("paystack charge collected but not persisted (reference %s): %v", e.Reference, e.cause)
}

func (e *ChargeCollectedError) Unwrap() error { return e.cause }

// NewChargeCollectedError builds a collected-but-unpersisted error for a charge reference.
func NewChargeCollectedError(reference string, cause error) error {
	return &ChargeCollectedError{Reference: reference, cause: cause}
}

// IsChargeCollected reports whether err (or anything it wraps) is a collected-but-unpersisted charge.
func IsChargeCollected(err error) bool {
	var target *ChargeCollectedError
	return errors.As(err, &target)
}

// MaskAuthorizationCode keeps Paystack authorization codes out of logs and API responses. Other
// values (Stripe payment method ids, empty strings) pass through unchanged.
func MaskAuthorizationCode(value string) string {
	return types.MaskGatewayPaymentMethodID(value)
}
