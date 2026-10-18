# Temporal namespace prerequisites and controlled recovery

Subscription workflows emit search attributes before their first activity.
`UpsertSearchAttributes` queues a command: nil does not prove server acceptance.
The worker's ten-second, namespace-scoped `ListSearchAttributes` startup preflight
is read-only and runs before schedule setup/polling. API and consumer modes are
unchanged. Missing/mistyped fields or denied reads intentionally block the worker.

## Attribute contract for the deployed store

Railway BSP reports `temporalio/auto-setup:1.26.2`, `DB=postgres12`,
`ENABLE_ES=false` (read-only verification, 2026-10-18). This is PostgreSQL SQL
visibility, not Elasticsearch. `postgres12` is a driver label, not the PostgreSQL
server's major version. Live schema/mapping introspection still requires existing
private-network access; stop if it contradicts this contract.

All five attributes use **Keyword**, with these write constraints:

- `SubscriptionID`, `TenantID`, `EnvironmentID`: exact identities, at most 255
  Unicode characters, valid UTF-8 and no NUL. Invalid visibility metadata is
  cleared, never truncated into a different identity. Workflow inputs and tenant
  authorization are unchanged.
- `FailingActivity`, `FailureReason`: exact-match previews, sanitized to valid
  UTF-8 without NUL and truncated to **255 Unicode characters including `...`**.
  Full errors remain in activity failures and existing logs. Equality searches
  for long errors must use the stored preview.

The [v1.26.2 PostgreSQL schema](https://github.com/temporalio/temporal/blob/v1.26.2/schema/postgresql/v12/visibility/versioned/v1.2/advanced_visibility.sql)
stores Keyword01–10 in `VARCHAR(255)`. Its Text01–03 fields are `TSVECTOR`, not an
unrestricted string replacement for arbitrary exception text. The
[SQL validator](https://github.com/temporalio/temporal/blob/v1.26.2/common/persistence/visibility/store/sql/visibility_store.go#L95)
does not enforce Keyword length. Keeping Keyword with bounded values preserves
existing equality queries and avoids both VARCHAR errors and Text parsing risks.
Even worst-case JSON escaping of 255 characters uses at most 1532 bytes including
quotes, below the default 2048-byte per-value limit. NUL is also invalid in
PostgreSQL JSONB. Do not change an existing type in place or edit SQL alias tables.

This changes upsert payloads only, preserving command count/order and workflow
control flow. The pinned SDK's
[replay matcher](https://github.com/temporalio/sdk-go/blob/v1.32.1/internal/internal_task_handlers.go#L1710)
compares the upsert event type, not its payload; accepted historical payloads are
not rewritten. No version marker or new billing command is added. Historical
oversized values/visibility failures need separate operator inspection.

## Fresh development/bootstrap environments

Compose has a `temporal-bootstrap` one-shot job using admin-tools 1.26.2. The
worker depends on its successful completion. `make dev-setup` invokes
`make init-temporal`; local workflow development should run that target after
starting Temporal. The Helm namespace bootstrap hook uses the same shell script
for each configured namespace. It lists before creating, skips matching fields,
refuses mistyped fields, and verifies the final contract. Registration failures
are not ignored. Worker startup itself still performs no registration.

**These are provisioning writes. Do not run `make init-temporal`, the bootstrap
container or the changed Helm upgrade hook against BSP's live backlog.** For an
existing Helm installation, review/contain its backlog before an upgrade that
runs this hook. Temporal Cloud/externally provisioned namespaces should keep
`temporalConfig.bootstrapNamespaces: []` and provision out of band.

## Operator runbook — no live steps executed by this PR

Every READ below is safe inspection; every WRITE requires a separately authorized
maintenance action. Keep histories/scoped business exports private. Never expose
the frontend or print credentials to establish access.

### 1. READ — establish identity, registry and producer inventory

Use an existing private-network shell. Set non-secret endpoint variables:

```sh
export TEMPORAL_ADDRESS=temporal.railway.internal:7233
export TEMPORAL_NAMESPACE=default
umask 077
temporal --address "$TEMPORAL_ADDRESS" operator cluster describe --detail
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" operator namespace describe
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" operator search-attribute list --output json > attributes-before.json
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" schedule list --output json > schedules-before.json
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" workflow count --query "WorkflowType IN ('ProcessSubscriptionBillingWorkflow', 'DraftAndComputeSubscriptionInvoiceWorkflow') GROUP BY ExecutionStatus"
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" workflow list --query "ExecutionStatus = 'Running' AND WorkflowType IN ('ScheduleSubscriptionBillingWorkflow', 'DailyDraftAndComputeWorkflow')" --limit 0 --output json > producers-open.json
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" workflow list --query "ExecutionStatus = 'Running' AND WorkflowType = 'ProcessInvoiceWorkflow'" --limit 0 --output json > invoices-open.json
```

Expected namespace ID: `93a0f1c6-ea99-4876-814a-c528dbc33d1d`; server 1.26.2 and
Postgres SQL visibility. Verify free Keyword slots for **five total** named fields;
three missing fields are proven, the two diagnostics need inspection. List every
schedule page (check CLI `--help`/limit and continuation behavior). Describe every
billing-producing action: `ScheduleSubscriptionBillingWorkflow` and
`DailyDraftAndComputeWorkflow`, including `subscription-billing`,
`daily-draft-and-compute`, and any `schtask_billing_*` actually present. Do not
infer their existence from names alone. Inventory open producer executions and
`ProcessInvoiceWorkflow` as well, using built-in WorkflowType/ExecutionStatus
filters. Record expected counts before changes; the 417-run sample is not a full
namespace census.
For CLI 0.13.2, `workflow list --limit 0` exhausts pagination; verify that
semantics against the installed CLI. Save each producer/invoice history with
`workflow show --workflow-id '<id>' --run-id '<run>' --output json`, and
authoritatively describe every pair. Invoice inputs identify invoice IDs; map
them back to scoped subscriptions through approved read-only business access.

### 2. WRITE — stop producers and worker polling BEFORE registration

Pause every identified billing/draft producer schedule by exact ID, preserving
its prior pause state. Example, **only after verifying this ID exists**:

```sh
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" schedule toggle --schedule-id subscription-billing --pause --reason 'P10 R2 controlled recovery'
```

Freeze manual billing/draft trigger calls for the maintenance window. Arrange an
approved stop of **all worker processes polling these queues**, including local
mode processes; the current shared Railway worker stop also interrupts cron,
credit-grant/payment-reconciliation/webhook work and requires an operator window.
Do not deploy/restart an older unbounded helper after registration. Pausing a
schedule alone leaves its already-started producer workflows and activities live.

### 3. READ — prove containment and build the complete exact-ID manifest

Verify identified schedules are paused, no producer is dispatching, all affected
pollers are stopped, and in-flight activities/external operations have settled.
A workflow termination cannot undo or reliably abort an already-running activity.
Inspect pending activities and their actual start-to-close deadlines; wait for
completion/deadline and reconcile persisted/external results. Do not proceed on a
worker process-status check alone. Open producer workflows must either finish
under controlled observation before the stop or be separately reviewed/terminated
and verified closed while workers are stopped. Record those exact IDs/counts too.

Build the read-only inventory binary from this PR for the operator host's OS and
architecture (for verified Linux amd64: `GOOS=linux GOARCH=amd64 CGO_ENABLED=0
go build -o ./temporal-recovery-inventory ./cmd/temporal-recovery-inventory`) and
transfer it through an existing authorized route. Do not transfer a macOS binary
to a Linux container. On the private-network host:

```sh
./temporal-recovery-inventory --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" > inventory-a.json
# Repeat after visibility/task lag has settled; compare exact workflow/run pairs.
./temporal-recovery-inventory --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" > inventory-b.json
python3 - <<'PY'
import collections, json
a, b = (json.load(open(p)) for p in ('inventory-a.json', 'inventory-b.json'))
pairs = lambda x: {(r['workflow_id'], r['run_id'], r['status'], r['decision']) for r in x['rows']}
assert pairs(a) == pairs(b), 'Inventory changed: do not approve a manifest'
groups = collections.Counter((r['tenant_id'], r['environment_id'], r['subscription_id'])
                             for r in b['rows'] if r['status'] == 'Running')
print('Counts:', b['counts'])
for scope, count in sorted(groups.items()):
    print(*scope, 'open=', count, 'canonical=NONE', 'proposed-terminate=', count)
PY
```

It exhausts list/history pagination, deduplicates workflow/run pairs, describes
each execution authoritatively, and reads **all** history events. It exports only
IDs, scope, counts and decisions (no raw customer payloads). Read RPCs are
sequential and paced below ten/second; individual calls time out after 30s and the
whole command after two hours. Errors produce no partial JSON manifest.

Expected `running = terminate-preactivity + manual-reconciliation`; closed
visibility rows are recorded as `closed-no-termination`. Reconcile the two exact
pair sets, visibility backlog/errors, and every run from the incident log sample
by authoritative describe (including those absent from visibility). If visibility
has not caught up, counts grow, or any read fails, stop: completeness is not proven.

### 4. READ — choose NONE as canonical and reconcile unsafe paths

The approved default is **zero old canonical runs per subscription**, across both
`ProcessSubscriptionBillingWorkflow` and
`DraftAndComputeSubscriptionInvoiceWorkflow`. Terminate all obsolete open runs,
including singletons; later recover from current verified state once per scoped
subscription. Choosing the oldest or newest run does not prove its safety.

`terminate-preactivity` requires a BadSearchAttributes failure and a complete
history containing only execution-start and workflow-task scheduled/started/
failed/timed-out events. A completed task, activity, signal, child, marker, or any
other event forces `manual-reconciliation`. Such rows are **not** cleared for
termination or replay automatically. Read scoped subscription/invoice/payment
rows and external operation results, then document the exact disposition before
approving the final manifest. Do the same for related invoice and producer runs.

Idempotency evidence and required manual controls:

- **Cancellation is not event-idempotent.**
  `activities/subscription/update_billing_period_activities.go:CheckCancellationActivity`
  tests dates without an already-cancelled guard, updates the row, then calls
  `ee/service/subscription.go:PublishCancellationEvents`. Each publication creates
  fresh event IDs for `subscription.updated` and `subscription.cancelled`. Check
  status/cancelled_at, resource termination, cancellation schedules, inherited
  subscriptions and event/outbox/delivery records. Already-cancelled rows must not
  get another billing-workflow replay. An uncertain event outcome needs manual
  event reconciliation, not a retry assumed safe.
- **Draft creation has scoped idempotency, invoice triggering does not.**
  `ee/service/invoice.go:CreateEmptyDraftInvoice` derives a scoped
  subscription/period/reason key, returns existing draft/skipped invoices and
  rejects finalized ones. `CreateDraftInvoicesActivity` appends returned IDs.
  `TriggerInvoiceWorkflowActivity` starts fresh IDs through
  `service/service.go:ExecuteWorkflowWithDelay` and
  `types/uuid.go:GenerateWorkflowIDWithContext`. Repeated parents can trigger
  multiple invoice-processing workflows. Inventory these by input invoice ID;
  reconcile duplicates, statuses, credits, provider attempts, payments and events
  before choosing at most one invoice processor. Compute skips immutable statuses
  and finalization locks/rechecks the row, but those guards are **not proof of
  exactly-once payment or event delivery**. Never replay paid/finalized invoices
  blindly or generate new provider idempotency keys for an uncertain charge.
- **Period advance is not safe under stale concurrent runs.**
  `CalculatePeriodsActivity` reads current DB periods;
  `UpdateCurrentPeriodActivity` writes its input dates without a compare-and-swap
  or monotonic guard. An old calculated period can overwrite a newer one.
  Compare stored periods to invoices/end/cancel dates and calculated catch-up
  periods; repair discrepancies through a separately approved scoped application
  path. Never allow old and replacement runs to overlap.

No old runs survive this plan, so no unsupported survivor-idempotency claim is
made. Activity response loss can still leave an uncertain write on a single new
run; inspect history **and** persisted effects before retrying a failed recovery.
This PR does not add financial/event deduplication or a per-subscription lock.

### 5. WRITE — terminate the reviewed obsolete runs BEFORE attributes

Create `termination-approved.json` from the complete reviewed inventory, retaining
exact `workflow_id`/`run_id` and scope, `canonical_run: ""`, and explicit
`decision: "terminate-approved"` per row. A human approval receipt must record
`N` old subscription runs, `P` producer runs and `I` related invoice runs and
resolve every manual-reconciliation row; do not silently filter them out.
For the **sample only**, N=417 over 24 subscriptions, keep=0, terminate=417;
393 are surplus over one/subscription. Actual N/P/I remain unknown until step 3.
Do not execute the sample as if it were the full current manifest.

For **each** approved exact pair, run (no wildcard/bulk query termination):

```sh
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" workflow terminate \
  --workflow-id '<approved workflow_id>' --run-id '<approved run_id>' \
  --reason 'P10 R2 obsolete duplicate backlog; canonical=none; reconciled before registration'
```

Expected successful terminations = approved still-Running N+P+I. A NotFound,
closed-state response or unexpected result requires a describe and count
reconciliation, not silently treating it as success. Keep the exact receipts.
Use terminate rather than cancellation requests, which run workflow cleanup and
need workers to process them. Termination itself does not undo existing effects.

### 6. READ — mandatory zero-backlog gate

Describe every approved pair: expected status `Terminated` (or an individually
explained pre-existing terminal status). Repeat the complete subscription
inventory: expected **running=0**, and independently verify **P/I open=0** for
all reviewed producers/invoice cohorts. Verify both subscription workflow types,
not just the 24 sampled subscriptions. Schedules remain paused, manual triggers
frozen, pollers stopped, and no activities are in flight. Compare visibility
results with authoritative describes and ensure visibility writes are healthy.
**Any unknown/unreconciled/Running old run blocks registration.**

### 7. WRITE — create only missing fields while pollers remain stopped

After the zero-backlog gate and permission/schema checks, issue the following
**only for names absent from attributes-before.json**:

```sh
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" operator search-attribute create --name SubscriptionID --type Keyword
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" operator search-attribute create --name TenantID --type Keyword
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" operator search-attribute create --name EnvironmentID --type Keyword
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" operator search-attribute create --name FailingActivity --type Keyword
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" operator search-attribute create --name FailureReason --type Keyword
```

Expected registrations: **3–5** if the observed missing three persist and the two
diagnostic fields are either absent or correct. Use the freshly read count,
not that estimate. An existing conflicting type or insufficient slots blocks the
step; do not remove/retype/reassign aliases or migrate Temporal SQL ad hoc.

### 8. READ — verify registry and select the exact worker build

List again: expected **5/5 required names Keyword**, no visibility errors, and
still zero old Running runs. The deployed worker's older source is 83ba98f; the
API's is c52905fe. Review the full worker delta or explicitly choose a backport
containing both the startup guard **and bounded write helpers**. Merging this PR
alone changes no Railway source pin. Preserve API/consumer source pins unless a
separate release is approved. Existing oversized history values are not repaired
by a new binary; investigate any visibility failure/backlog before proceeding.
Check `AllTemporalScheduleConfigs` in the selected build as well: startup's
`EnsureSchedules` preserves an existing schedule's pause state but creates a
missing schedule active. Both built-in billing/draft producers must already
exist and be paused before this build starts. If absent, stop and separately
provision them paused with their exact reviewed spec/action; do not allow startup
to create an active producer during recovery. Include any other producer found
to mutate the same subscriptions/invoices in the containment review.

### 9. WRITE — deploy the bounded helper and start controlled recovery

Deploy only the separately approved exact worker build/pin. Do not resume the
old worker after registration. Leave billing/draft schedules paused and manual
bulk triggers frozen. Optional throughput limits are **workflow-task size 2**,
activity execution size 1 and worker activity rate 1/sec:

```text
FLEXPRICE_TEMPORAL_WORKER_MAX_CONCURRENT_WORKFLOW_TASK_EXECUTION_SIZE=2
FLEXPRICE_TEMPORAL_WORKER_MAX_CONCURRENT_ACTIVITY_EXECUTION_SIZE=1
FLEXPRICE_TEMPORAL_WORKER_WORKER_ACTIVITIES_PER_SECOND=1
```

Never use workflow-task size 1. The pinned
[Go SDK v1.32.1 validation](https://github.com/temporalio/sdk-go/blob/v1.32.1/internal/internal_worker.go#L1660-L1675)
panics at 1 for both workflow-task execution size and workflow-task pollers,
because sticky polling would starve normal requests. These optional limits apply
per worker/task queue, including unrelated cron/credits/webhooks; they are load
controls and **do not serialize entire workflows or prevent duplicates**.
Containment is steps 2–6, not these settings.

Submit one reviewed scoped subscription recovery at a time using the ordinary
approved application trigger and freshly verified current state. Expected open
recovery count is **at most 1** for that tenant/environment/subscription across
both subscription workflow types. No old run is reset/resumed. If any step
fails or its result is uncertain, freeze that subscription and perform the
manual checks from step 4; do not automatically submit another run. Handle
already-cancelled subscriptions and existing unprocessed invoice IDs through the
reviewed scoped paths, not a blind full-workflow retry.

### 10. READ — prove real billing results before the next subscription

For each recovery receipt: expected period monotonically correct, no duplicate
invoice per scoped period/reason, expected drafts/line items/totals, cancellation
state/resources and event delivery reconciled once, and at most one downstream
invoice processor per invoice. Wait the designed **15–20-minute** invoice trigger
delay and inspect finalization, credits and actual provider/payment results.
Search-attribute success or worker health alone is insufficient. Do not advance
until the subscription has no Running workflow and all uncertain activity/event/
payment results are resolved. Read-only checks must include failed/closed runs
from the Postgres outage; missing-attribute recovery does not repair them.

### 11. WRITE — restore approved producers and normal limits

Only after all scoped reconciliation passes, unpause precisely the schedules
that were previously active (preserve previously paused ones), restore agreed
worker throughput, and lift the manual-trigger freeze. Verify no remaining open
producer will overlap the first schedule action before unpausing. Example:

```sh
temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" schedule toggle --schedule-id subscription-billing --unpause --reason 'P10 R2 reconciled backlog'
```

### 12. READ — observe subsequent cycles and the Airtel cutoff

Verify zero BadSearchAttributes/visibility-write errors, healthy Postgres disk/
load and task backlog, correct next schedule actions and several successful
two-minute scans without duplicate open subscription/invoice runs. The fresh-ID
producer design still permits future overlap if processing takes longer than its
scan interval; if observed, recontain and require a per-subscription workflow/
event idempotency change before unattended billing. This runbook is not that fix.

Read Airtel `subs_01M4TJ8X5BVJPWXFHCB4JX6DMY` in production environment
`env_01M0A3XZCR4VXCJ7NDFC92H1FE`, customer
`cust_01M4TJ8QDSH89448T4J80KCFPR`, using existing scoped BSP access. Record
current periods, end/cancel fields, invoices and collection mode; do not change
customer dates for a smoke. Before October 27, prove the end-date path in an
isolated environment or another representative approved case. At/after
**2026-10-27 12:02:17.567 UTC**, verify intended cancellation, no unintended
renewal/charge, and independent Auth trial expiry. Any extension/conversion or
manual financial adjustment needs its own commercial/operator decision.
