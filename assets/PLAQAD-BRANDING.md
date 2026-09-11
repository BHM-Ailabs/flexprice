# Plaqad invoice assets

`typst-templates/plaqad-logo.png` is the official transparent Plaqad logo, copied
from Plaqad-Website `public/brand/plaqad-logo-navbar.png`.

`fonts/GoogleSans-Regular.ttf` is the existing Account Google Sans font, decoded
from Plaqad-auth `apps/api/src/generated/billing-font.ts`. The Auth generator
derives that font from the locally bundled Account Google Sans asset. No remote
font request or Typst package download is required when rendering an invoice.

The Plaqad template uses the brand blue `#209DD8`. Branding is limited to the
configured Plaqad tenant. The invoice QR points to authenticated Account invoice
status; it does not create or imply a new payment link.
