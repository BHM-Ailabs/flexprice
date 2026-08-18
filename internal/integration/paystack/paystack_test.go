package paystack

import (
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidWebhookSignature(t *testing.T) {
	payload := []byte(`{"event":"charge.success","data":{"reference":"fp-pay-1"}}`)
	mac := hmac.New(sha512.New, []byte("sk_test_example"))
	_, _ = mac.Write(payload)
	signature := hex.EncodeToString(mac.Sum(nil))

	require.True(t, validWebhookSignature(payload, signature, "sk_test_example"))
	require.False(t, validWebhookSignature(payload, signature, "wrong-secret"))
	require.False(t, validWebhookSignature(payload, "not-hex", "sk_test_example"))
}

func TestReferenceForPaymentID(t *testing.T) {
	tests := []struct {
		name      string
		paymentID string
		want      string
	}{
		{name: "FlexPrice identifier", paymentID: "pay_01ABC", want: "fp-pay-01ABC"},
		{name: "unsafe characters", paymentID: "pay /?# 10", want: "fp-pay-10"},
		{name: "empty", paymentID: "", want: "fp-payment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.want, referenceForPaymentID(test.paymentID))
		})
	}

	long := referenceForPaymentID(strings.Repeat("x", 200))
	require.Len(t, long, 100)
}
