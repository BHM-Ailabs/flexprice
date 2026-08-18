# Paystack integration

The Plaqad FlexPrice fork supports Paystack as a native hosted-checkout gateway. FlexPrice remains the source of truth for invoice amounts and payment state; Paystack hosts checkout and confirms settlement.

## Current scope

- Hosted payment links for `PAYMENT_LINK` payments
- FlexPrice Checkout sessions using `payment_provider: "paystack"`
- Encrypted Paystack credentials per FlexPrice environment
- HMAC-SHA512 webhook authentication
- Server-side transaction verification before reconciliation
- Amount and currency matching before an invoice or checkout is completed
- Idempotent handling of duplicate `charge.success` deliveries

Saved-card authorization and automatic off-session charging are intentionally disabled. They require a separate mandate, retry, and customer-consent review.

## Create the connection

Create one connection in the FlexPrice environment that will collect payments:

```http
POST /v1/connections
Content-Type: application/json
x-api-key: <flexprice-admin-key>

{
  "name": "Plaqad Paystack",
  "provider_type": "paystack",
  "encrypted_secret_data": {
    "secret_key": "<paystack-secret-key>",
    "public_key": "<paystack-public-key>"
  }
}
```

The public key is optional for server-hosted checkout. The secret key is required and is encrypted at rest.

## Configure the webhook

In Paystack, configure this URL for the relevant test or live integration:

```text
https://<flexprice-api-host>/v1/webhooks/paystack/<tenant_id>/<environment_id>
```

Subscribe to `charge.success`. FlexPrice verifies `x-paystack-signature` with the connection's Paystack secret key and then verifies the transaction through Paystack's API before changing billing state.

## Create a payment link

```http
POST /v1/payments
Content-Type: application/json
x-api-key: <flexprice-api-key>

{
  "amount": "15000",
  "currency": "NGN",
  "destination_id": "<invoice_id>",
  "destination_type": "INVOICE",
  "payment_method_type": "PAYMENT_LINK",
  "payment_gateway": "paystack",
  "process_payment": true,
  "success_url": "https://account.plaqad.com/billing"
}
```

The response's payment gateway metadata contains the hosted checkout URL. The connector uses a deterministic, Paystack-safe reference derived from the FlexPrice payment ID.

## Production activation checklist

1. Obtain FlexPrice approval for a Production environment.
2. Create a Production-scoped FlexPrice API key and Paystack connection.
3. Use Paystack test keys first and complete an end-to-end invoice settlement.
4. Confirm duplicate webhook delivery leaves the invoice and payment unchanged.
5. Replace the connection with live Paystack keys only after approval.
6. Configure the live Paystack webhook URL and perform a small controlled transaction.

Do not copy Sandbox customers, synthetic events, or test connections into Production.
