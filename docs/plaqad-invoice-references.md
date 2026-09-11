# Plaqad invoice references

Native BSP owns the short public reference. The first number in each configured Plaqad tenant/environment is `100001`; the counter increases without a prefix, date, reset or reuse. Existing `invoice_number`, invoice IDs, payment IDs, financial fields and settlement state remain unchanged.

## Storage and migration

`ent/schema/invoicepublicreference.go` and `invoicereferencealias.go` own two additive tables. `migrations/ent/migration_20260911183848.sql` was generated with `make generate-migration` against an isolated PostgreSQL instance after applying the existing schema. It contains only two CREATE TABLE statements and five CREATE INDEX statements. It does not alter invoice/payment tables. Generated Ent sources came from `make generate-ent`; `go.mod` is unchanged and the extra `go.sum` entries are generator transitive checksums.

The existing atomic `invoice_sequences` counter uses the reserved six-character bucket `PUBLIC`, starting at 100001. Do not delete, reset or reuse this bucket. It is independent of all date-number configuration; the existing chronological cleanup predicate does not select it. A transaction-level advisory lock scopes reservation, first customer attachment, binding and alias creation to the tenant/environment. The registry and aliases retain original actor/time. Binding may set an empty customer or invoice pointer once; there are no update/delete/renumber endpoints.

Before production release, compare the generated DDL with a read-only production migration dry run. Apply only these reviewed additive statements if any unrelated schema drift appears. The existing startup migration skips column/index drops and index modifications, but that is not a substitute for reviewing the production DDL. Deploy API code only after both tables and indexes exist. Rollback may return to the preceding API image without dropping the registry, aliases or counter.

Release verification on 2026-09-11 captured the actual production PostgreSQL 18.6 schema over a read-only connection, restored that schema (no customer data) into isolated PostgreSQL 18, and ran the current source migration dry run. The resulting seven statements exactly matched the reviewed SQL: two new tables and five indexes, zero ALTER/DROP or unrelated changes. The required subscription invoicing-customer column and counter unique index were verified; 57 existing invoices had no duplicate legacy-number groups. Protected invoice/payment rows were captured privately for the after-migration comparison.

## API contract

All routes use existing private invoice write authorization and additionally require the configured Plaqad tenant with a nonempty environment; customer-portal contexts cannot administer references. Success is HTTP 200. Invalid evidence fails closed; it never adopts a second number or changes financial records.

`POST /v1/invoices/references/reserve`

```json
{"reservation_key":"plaqad-prepaid:pinv_example","expected_customer_id":"cust_example","aliases":["OLD-ISSUED-NUMBER"]}
```

`expected_customer_id` may be omitted before customer claim. Call the same reservation after verifying the customer and before native invoice/subscription creation to attach it permanently. A repeated reservation key returns its original number, including after a lost response. Aliases are optional, at most 20 per call, each 1–200 characters without NUL/newlines/tabs. Alias matching is case insensitive; an alias already owned by a different reference is rejected.

```json
{"public_reference":"100001","reservation_key":"plaqad-prepaid:pinv_example","invoice_id":null,"customer_id":"cust_example","aliases":["OLD-ISSUED-NUMBER"]}
```

Include `metadata.plaqad_public_reference_key` with that reservation key on native one-off invoice or subscription creation. A one-off invoice adopts that reservation before the API exposes the invoice. For a subscription, only `SUBSCRIPTION_CREATE` uses the parent's reservation; later cycle/trial/update invoices receive fresh references even if copied metadata contains the key. A missing reservation or wrong/unattached customer fails closed, rather than allocating another opening reference.

`POST /v1/invoices/:id/reference` accepts `{expected_customer_id, reservation_key?, aliases?}` and returns the same shape. It verifies the real native customer and scope. Binding is permitted only when the reservation is unbound or already belongs to that invoice, and the invoice does not have a different reference. Use this to register historical Auth document aliases after verifying payment attribution.

Invoice reads return additive `public_reference` and `reference_aliases`. A historical attribution conflict returns a present empty `public_reference` (display **Number pending**) without hiding the financial invoice or its neighbours; database/security errors still propagate. Create and explicit Bind remain strict, and an unavailable reference cannot start a new Stripe checkout. Payment invoice projections return the same fields. Other tenants omit them. Canonical references are display/search identifiers, never authorization credentials; links and settlement continue to use internal IDs.

## Older workers and search

Current API repository reads materialize missing references for old or new worker-created invoices, using writer-side authoritative metadata. No unrelated worker deployment is required. Bound references use an authoritative writer read without the allocation lock. Read projections inside a financial transaction do not allocate; `GetForUpdate` and cross-scope scheduled-job enumeration retain their original financial/job behavior. The materializer is also used by list/export, PDF and payment/Stripe-hosted checkout reads. A new invoice created through current API is assigned before its response. Portal access checks must still scope the customer; a wrong portal customer cannot allocate or inspect a reference.

An attached but not-yet-bound reservation can resolve an old-worker invoice through the same scoped customer and authoritative metadata evidence before its first Get/Bind.

Top-level invoice `search` matches public reference, old native number, appended aliases, internal invoice ID and idempotency key. The new virtual typed `invoice_reference` filter supports `eq` and `contains` with the same OR semantics. Existing `invoice_number` filters keep their old meaning. All date/currency/customer/tenant/environment constraints still intersect this search. Portal invoice POST accepts `{page,limit,search}` (limit at most 100, search at most 200 characters) and returns normal server pagination.

## Backfill

`POST /v1/invoices/references/backfill` accepts `{after_id:"",limit:100,apply:false}`. The default is a read-only bounded inventory plan: no reference is allocated. The response contains `invoice_ids`, `next_cursor`, `count`, `apply`, `conflicts`, and `financial_rows_changed:false`. Limit is 1–200; continue using the returned last ID until an empty page.

After reviewing scope and the unchanged protected financial snapshot, repeat each planned page with `apply:true`. Each assignment is transactional and idempotent. Validation conflicts are collected as `{invoice_id,reason}` while other rows continue; a database failure can leave earlier rows assigned, so retry the same page without deleting state. Resolve reserved prepaid metadata before retrying a reported attribution conflict. Auth separately registers legacy document aliases and projects the verified native reference. Do not resend receipts or recreate invoices to change display numbers.

## Validation

Real PostgreSQL tests cover concurrent reservations, retry recovery, customer attachment, wrong tenant/environment/customer, double binding, canonical and old-alias search/count, idempotent dry/apply backfill, and opening versus recurring-cycle references. Entire original invoice rows are compared before/after registry changes. Handler and existing PDF/portal checks cover boundary behavior. Synthetic data only; no provider payment or email operations are part of these checks.
