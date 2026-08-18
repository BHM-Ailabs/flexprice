package types

import "strings"

// PaystackAuthorizationCodePrefix is the fixed prefix Paystack gives every card authorization
// code. Such a code is a bearer credential: whoever holds it can charge the customer's card, so
// it must never be logged or returned over the API.
const PaystackAuthorizationCodePrefix = "AUTH_"

// IsPaystackAuthorizationCode reports whether a gateway payment method id is a Paystack card
// authorization code rather than, say, a Stripe payment method id.
func IsPaystackAuthorizationCode(value string) bool {
	return len(value) > len(PaystackAuthorizationCodePrefix) && strings.HasPrefix(value, PaystackAuthorizationCodePrefix)
}

// MaskGatewayPaymentMethodID redacts Paystack authorization codes; every other value (Stripe
// payment method ids, empty strings) passes through unchanged.
func MaskGatewayPaymentMethodID(value string) string {
	if IsPaystackAuthorizationCode(value) {
		return "[paystack authorization]"
	}
	return value
}

// RedactGatewayPaymentMethodID returns a response-safe copy of a gateway payment method id.
func RedactGatewayPaymentMethodID(value *string) *string {
	if value == nil {
		return nil
	}
	masked := MaskGatewayPaymentMethodID(*value)
	return &masked
}
