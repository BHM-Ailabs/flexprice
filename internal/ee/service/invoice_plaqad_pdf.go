package service

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"

	"github.com/flexprice/flexprice/internal/domain/invoice"
)

// Branding follows the explicitly configured Plaqad tenant, including when SSO
// is temporarily disabled during a rollback. Other tenants retain their PDFs.
func (s *invoiceService) isPlaqadInvoiceTenant(tenantID string) bool {
	return s.Config != nil && s.Config.Auth.Plaqad.TenantID != "" && s.Config.Auth.Plaqad.TenantID == tenantID
}

var plaqadInvoiceID = regexp.MustCompile(`^inv_[A-Za-z0-9]{1,100}$`)
var plaqadWorkspaceID = regexp.MustCompile(`^ws_[A-Za-z0-9]{1,100}$`)

func plaqadInvoiceAccountLink(invoiceID, customerExternalID string) (string, string) {
	// The joined native customer supplies the workspace hint. Account must still
	// verify the signed-in user's membership and the exact invoice mapping.
	if plaqadInvoiceID.MatchString(invoiceID) && plaqadWorkspaceID.MatchString(customerExternalID) {
		return "https://account.plaqad.com/app/billing/invoices/" + invoiceID + "?" + url.Values{"workspaceId": {customerExternalID}}.Encode(), "View this invoice in your Plaqad account"
	}
	return "https://account.plaqad.com", "Open your Plaqad account"
}

func plaqadInvoicePDFCacheKey(inv *invoice.Invoice) string {
	// Version both the template and the invoice snapshot. A payment/status change
	// must not return an older unpaid PDF; never overwrite or delete old evidence.
	// Invoice contains only JSON-serializable domain values.
	snapshot, _ := json.Marshal(inv)
	fingerprint := sha256.Sum256(snapshot)
	return fmt.Sprintf("%s/%s/plaqad-invoice-v1/%s/%x", inv.TenantID, inv.EnvironmentID, inv.ID, fingerprint[:16])
}
