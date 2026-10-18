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

## Local regression checks

`GOMAXPROCS=2 make test-temporal-bootstrap` runs the fake-CLI provisioning tests and the real-chart loader/engine tests (directory and packaged archive, plus missing-script failure). Helm test dependencies live in an isolated Go module under `internal/ee/infrastructure/helm/tests`; they do not enter the production module graph. Run `GOMAXPROCS=2 go test -race -p 2 ./internal/temporal/... ./cmd/temporal-recovery-inventory/...` for the Temporal and inventory checks. These tests perform no infrastructure writes.

## Round 3 — complete operator runbook (2026-10-18)

**This replaces every Round 1/2 execution procedure, including the old 15–20-minute finalization claim. No live action in this runbook was executed.** Repository source is `/Users/kodaas/Code/BHM/flexprice-temporal`, PR #5, target `codex/bsp-scoped-sso`; PR #4 and its checkout are outside this change. READ/WRITE labels describe effects on live services; building binaries and saving private evidence are local filesystem writes. All WRITE steps below are future, separately authorized operator actions.

The recovery policy is **no old canonical billing/draft run**, then one reviewed replacement per tenant/environment/subscription. Attribute registration is an activation boundary: remaining blocked runs would become executable. Never register while any old affected execution or unsettled activity remains. This change bounds visibility metadata; it does not make cancellation events, payments or concurrent period writes exactly once.

### 0. READ — approve source, images, access and time budgets before containment or any stop

**Preconditions:** existing authorized private Temporal and scoped business DB access; no public endpoint or new research credential. Verify namespace UUID `93a0f1c6-ea99-4876-814a-c528dbc33d1d`, server/store identity, PG health/disk/WAL headroom and available backup destination. Treat all counts below as unknown until read; 417 runs/24 subscriptions is only a historical sample.

Choose **backport vs promotion now**. The recorded worker backend pin is `83ba98f669c9dc252064152e975104aef9e599c7`; API pin is `c52905feb4b315b7062b2624d55cc178e35ef7f1`. Review the complete chosen source delta, not just `internal/temporal`. A backport must contain the startup registration guard **and both bounded search-attribute writers**, plus any compatibility fixes necessary for that base. Record the exact wrapper revision, explicit `FLEXPRICE_SOURCE_REF`, build SHA and immutable image digest; do not use a wrapper's changing default pin. Build/test the candidate and a rollback image **before stopping anything**. Both images must preserve bounded writes and replay compatibility. The recovery candidate must retain the fail-fast guard. To cover a failed **partial** registration, the staged fallback must be an explicitly reviewed **bounded-writer backport without the strict startup guard**, restricted to unrelated worker service with all billing producers/callers held and zero affected executions. A second guarded image alone cannot restore service when only some fields were registered. Do not invent an environment override: none exists in this PR. Before that fallback processes any billing work, independently verify the full five-field contract or switch to the guarded candidate. Rehearse startup and existing-schedule pause preservation in an already-approved isolated namespace; no production bootstrap is a rehearsal. Review every `AllTemporalScheduleConfigs` entry in both images.

Build the inventory for the maintenance host's verified OS/architecture, for example:

```sh
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o ./temporal-recovery-inventory ./cmd/temporal-recovery-inventory
```

Transfer binaries/images through existing authorized channels. Record tested rollback/start commands for the actual worker service, replica counts and all other local/remote pollers. This report supplies no Railway deployment command or authorization.

Set two explicit budgets: (a) the producer/collection hold, sized from the full census (may be hours/days), with customer/payment operations coverage; (b) **maximum shared-worker stop of 30 minutes**, with fallback initiated at minute 10 if readiness is not established. A different limit requires approval **before** entry. Long census/export/termination work belongs before the short stop. A 2-hour invoice delay is handled with the worker running, never by extending its stop. The release owner must supply candidate/fallback digests and an enforceable API/integration/dunning freeze; absent any of these, **no-go before any stop or termination**.

**Expected/verify:** two tested immutable images, one source decision, access verified without displaying secrets, approved budgets/owners and fallback commands. **Rollback:** no live change; abandon preparation.

### 1. READ — census every schedule, workflow, caller and financial policy

Use the existing private-network host; credentials stay in its approved client configuration. Use a private directory on a backup volume, not the Postgres service disk:

```sh
umask 077
export TEMPORAL_ADDRESS=temporal.railway.internal:7233
export TEMPORAL_NAMESPACE=default
t() { temporal --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" "$@"; }
temporal --version
t operator cluster describe --detail > cluster-before.txt
t operator namespace describe --output json > namespace-before.json
t operator search-attribute list --output json > attributes-before.json
t schedule list --output json > schedules-before.json
t workflow count --query "WorkflowType IN ('ProcessSubscriptionBillingWorkflow', 'DraftAndComputeSubscriptionInvoiceWorkflow') GROUP BY ExecutionStatus" --output json > subscription-counts-before.json
t workflow list --query "ExecutionStatus = 'Running'" --limit 0 --output json > all-open-before.json
```

The pinned [CLI 0.13.2 schedule implementation](https://github.com/temporalio/cli/blob/v0.13.2/temporalcli/commands.schedule.go) exhausts schedule pages; workflow `--limit 0` means unlimited. Confirm installed `--help`/version before using these commands. For a different CLI, verify pagination explicitly. Check every command's exit status and JSON validity; incomplete stdout after a failed command is not evidence of completeness. Compare complete lists with workflow counts and authoritative describes; investigate visibility lag/errors.

Check all five required names (`SubscriptionID`, `TenantID`, `EnvironmentID`, `FailingActivity`, `FailureReason`): existing type must be Keyword, enough SQL Keyword slots must be free for **all missing** names. Record exact missing count M (0–5; historical evidence proves only three missing). Type conflicts or slot shortages block the procedure **now**, before termination or downtime. Record actual namespace retention; 24h is auto-setup's default, **not a verified production value**.

For **every returned schedule ID**, save its complete action/input/spec, prior pause state and recent/running action pairs:

```sh
t schedule describe --schedule-id '<exact observed ID>' --output json > '<private numbered schedule file>.json'
```

Build `schedule-manifest.json` with `id`, `was_paused`, `action`, `queue`, `input`, `spec`, `hold`, reason and owner. Names alone never prove actions. Code-owned producer inventory at the reviewed source:

| Schedule/caller | Action and containment |
|---|---|
| `subscription-billing` (2m); API-created `schtask_billing_*` (15m) | `ScheduleSubscriptionBillingWorkflow` → fresh `ProcessSubscriptionBillingWorkflow`; pause **every** matching schedule, regardless of ID. |
| `daily-draft-and-compute` (02:00 UTC daily) | `DailyDraftAndComputeWorkflow` → `DraftAndComputeSubscriptionInvoiceWorkflow`; pause. |
| API-created `schtask_draft_finalize_*` (30m) | `ScheduleDraftFinalizationWorkflow` → `FinalizeDraftInvoiceWorkflow` → finalization/payment; pause **all instances**. Not installed by `EnsureSchedules`; zero instances is a possible live finding, not proof that finalization happens automatically. |
| `subscription-auto-cancellation` (15m) | `SubscriptionAutoCancellationWorkflow` → immediate nonpayment cancellation; pause for the entire recovery and invoice-grace reconciliation. |
| `subscription-auto-invoice-threshold-billing` (5m) | `AutoInvoiceThresholdBillingWorkflow` → mid-period invoice path; pause. |
| `subscription-trial-end-due` (15m) | `SubscriptionTrialEndDueWorkflow` → subscription/trial invoice changes; pause. |
| `subscription-renewal-due-alerts` (15m) | `SubscriptionRenewalDueAlertsWorkflow`; hold customer reminders during reconciliation. |
| `paddle-invoice-pull-sync` (hourly) | `PaddleInvoicePullSyncCronWorkflow` → `PaddleInvoicePullSyncWorkflow`; pause invoice import/sync. |
| `marketplace-usage-snapshot` (6h), `marketplace-usage-report` (3h) | `MarketplaceUsageSnapshotWorkflow`, `MarketplaceUsageReportWorkflow`; hold billing/reporting effects and review existing `MarketplaceSubscriptionFinalUsageFlushWorkflow`. |
| `webhook-stale-retry` (2m) | `OutboundWebhookStaleRetryWorkflow`; pause stale delivery/retry, including invoice communications. This alone does **not** stop immediate delivery in API/consumer/worker routers. |
| `moyasar-auth-payment-settlement` (15m) | `MoyasarAuthPaymentSettlementWorkflow` / `ReconcilePendingAuthPaymentsActivity`; hold/reconcile pending customer-payment settlement if it touches the cohort. Conservatively hold until the owner proves it independent. |
| `credit-grants-processing`, `wallet-credit-expiry` (15m), `checkout-session-expiry` (30m) | Credit/expiry/checkout maintenance may remain active **only** after action/input review proves no subscription/invoice producer. They still stop briefly with the shared worker. Capture their balances/expiry changes in financial reconciliation. |
| Any other schedule, legacy cron or unknown action | Describe and follow its complete call chain. Pause if it can write/start billing, finalization, cancellation or period work; unknown means blocked, not exempt. |

Sources: `internal/types/schedule.go`, `internal/temporal/service/schedules.go:25–123`, `internal/ee/service/scheduled_task.go:783–866`, `internal/temporal/activities/{subscription,invoice,cron}`. Inspect the **selected live image's** registration/call graph too; do not assume an older worker registers every newer workflow.

Inventory all open types, including legacy `SubscriptionBillingPeriodsWorkflow` → `UpdateBillingPeriodsActivity` → `UpdateBillingPeriods` (period advance, cancellation and pending plan changes), subscription creation/change workflows, `TaskProcessingWorkflow`, `StripeIntegrationWorkflow`, Paddle subscription sync, invoice compute/recalculate/finalize/process, integration/provider invoice sync/mark-paid, and marketplace flush. Read each action/input/history and map it to scoped subscription/invoice IDs. Use `all-open-before.json` to catch custom/older workflow names and Temporal **CronSchedule** executions, retries, child starts and delayed `ProcessInvoiceWorkflow` starts; schedule pause does not contain these. Pending DB subscription schedules are executed by `ProcessPendingPlanChangesActivity`; scheduled task force-run and external API callers can also bypass server-schedule pause.

Inventory and freeze the following call surfaces, including UI, machine clients and external scheduler owners:

- `/v1/subscriptions/temporal/schedule-update-billing-period`, `/schedule-draft-finalization`, `/:subscription_id/trigger-workflow`, `/:subscription_id/draft-and-compute`;
- subscription create/cancel/change/modify, line-item and subscription-schedule mutations; invoice create/compute/finalize/recalculate, `/temporal/:invoice_id/finalize`, payment attempt/status, `/v1/payments` mutations/process, and `/v1/tasks/scheduled` create/update/run;
- invoice comms/webhook trigger and webhook retry, integration sync and incoming provider webhooks; Kafka integration/system-event consumers that can start these workflows (including API-mode routers, not only the worker);
- any externally configured `/v1/cron/...` caller or direct Temporal start/trigger/backfill/reset/update client. Current `internal/api/router.go` has no registered `/v1/cron` group; stale activity comments are not proof it exists in the deployed revision. Read actual revision and external cron inventories.

Sources: `internal/api/router.go`, `internal/api/v1/{invoice,scheduled_task,subscription}.go`, `internal/integration/events/{handler,dispatch}.go`, `internal/ee/service/{subscription_schedule,subscription_trial,subscription_change,subscription_modification*}.go`. Record exact caller/cron counts and their owner-provided stop/resume commands. There is no per-cohort maintenance switch in this PR. An operator promise to avoid clicking the UI cannot substitute for disabling automated callers and integration dispatch. Preserve inbound provider notifications durably for later reconciliation; do not acknowledge/drop them to fake containment.

Read scoped subscription status/period/end/cancel/trial fields, inherited subscriptions, invoice IDs/status/period/reason/due dates/last-computed times, payments/provider attempts, wallet/credit balances, events/outbox/delivery records, tenant invoice terms and `subscription_config.auto_cancellation_enabled/grace_period_days`. No cross-tenant unscoped business query. Read-only support views/APIs or an approved read-only DB session must enforce both tenant and environment filters.

**Expected/verify:** S total schedules; S_hold exact held IDs; P open producers; I invoice/finalizer/compute runs; R other mutators; N old billing/draft runs; C scoped subscriptions and M missing fields. Each needs a full live count, not a sample. Unknown caller/action/policy, conflicting fields or inaccessible business evidence blocks writes. **Rollback:** none, READ only.

### 2. WRITE — contain all producers and collection/dunning before any termination or registration

**Preconditions:** step 0 images/budgets and step 1 complete signed manifests. Activate the approved caller/ingress/consumer holds from step 1, covering direct API writes and provider/system-event starts. Keep independent reads/usage ingestion available only where the isolation is proven. Apply the exact owner-supplied commands from the freeze receipt; no unsupported environment flag is implied here.

For **each exact ID with `hold=true`**, preserving its previous state:

```sh
t schedule toggle --schedule-id '<observed manifest ID>' --pause --reason 'P10 R3 contained recovery'
t schedule describe --schedule-id '<same ID>' --output json > '<private paused receipt>.json'
```

This includes all finalization schedules and auto-cancellation. Keep auto-cancellation paused namespace-wide because its activity scans all tenant environments and has no cohort-exclusion argument. Hold reminders, manual collection/retry calls and external provider dunning for affected invoice/subscription IDs. There is no independent dunning Temporal schedule in this source: invoice communication events (`invoice.go:3742+`) and the immediate webhook/integration delivery paths can activate downstream dunning. The operator must record the actual Svix/native/customer/provider hold and verify no affected notifications/retries escape. If those holds cannot be established, **do not create recovery invoices**.

**Expected/verify:** S_hold/S_hold schedules paused; zero unaccounted callers. New starts can still arise from already-started producer activities; step 3 must prove they have settled. **Rollback:** before irreversible steps, restore only previously active schedules/callers after reviewing outstanding effects. Do not automatically unpause financial work merely because the window is aborted; leave it held with an assigned owner if safety is uncertain.

### 3. READ — prove quiescence while retaining unrelated worker service

**Preconditions:** all holds verified. Re-list schedules and **all** open workflows, describe every relevant exact pair, inspect timers/pending activities/retries and actual activity timeouts. Allow already-started producer/invoice/integration work to finish only under explicit approved observation; reconcile every persisted/external result. Any unapproved ongoing operation blocks the optimized path.

```sh
t schedule list --output json > schedules-contained.json
t workflow list --query "ExecutionStatus = 'Running'" --limit 0 --output json > all-open-contained.json
t workflow describe --workflow-id '<exact ID>' --run-id '<exact run>' --output json > '<private describe>.json'
t workflow show --workflow-id '<exact ID>' --run-id '<exact run>' --output json > '<private history>.json'
```

The old worker may stay running through steps 4–7 **only if** all other producer/mutator executions are closed, their activities/external calls settled, and every remaining affected run is proven blocked at its first upsert by the still-missing attributes. No registry change is permitted during these steps. Pause/termination never cancels an in-flight external call reliably. Missing attributes protect only the two upserting workflow types, not finalizers/cron/integration workflows.

If P/I/R cannot drain safely, or any subscription run has accepted commands/activities, **do not use the keep-worker-running path**. Stop here, size/reapprove a separate maintenance window for stopping affected pollers, preserving evidence and reconciling/disposing those exact runs using steps 4–7. Do not exceed the agreed stop budget to work out unknowns. Without isolation or an approved longer window, recovery stays blocked with holds retained; this runbook does not invent queue isolation.

**Expected/verify:** new affected starts=0 over at least two producer intervals plus observed activity/visibility lag; P/I/R open=0, unsettled external effects=0. Remaining N runs strictly preactivity. Unrelated credit/expiry/checkout work can run; settlement/reminders held by policy remain deferred. **Rollback:** no new writes here; on failed containment retain holds, recensus and amend the plan. Do not infer quiescence from process status alone.

### 4. READ — take private backups and scalable, resumable full inventories

**Preconditions:** stable containment, compatible attributes, export capacity and read-only DB session. Before any irreversible termination, take verified read-only logical backups (or reference an existing completed recoverable snapshot) of business and Temporal persistence/visibility DBs. Example uses existing libpq service entries, not passwords/URLs in commands:

```sh
pg_dump --version
pg_dump --dbname='service=bsp-business-read' --format=custom --file='./private-backup/business-before.dump'
pg_dump --dbname='service=bsp-temporal-read' --format=custom --file='./private-backup/temporal-before.dump'
# If visibility is a separate DB, use its independently verified read-only service entry too.
pg_dump --dbname='service=bsp-visibility-read' --format=custom --file='./private-backup/visibility-before.dump'
pg_restore --list './private-backup/business-before.dump' > './private-backup/business-before.contents'
```

Use a PG client compatible with the verified server. Resolve actual DB/service names first (`temporal`, business `railway`, visibility DB not yet verified); omit the third dump only if visibility is proven in the same DB. Do not write dumps onto the full Postgres volume. Record backup timestamps, checksums and restore-readability evidence privately; a consistent DB dump is not a financial rollback and does not capture external payments. Also export scoped business baselines from step 1 and all retained outage-era closed histories. Missing/purged history is an explicit evidence gap requiring business/provider reconciliation, never proof of no effects.

Size from full N and sampled history page counts **before** choosing deadlines. The inventory is sequential, globally below 10 read RPC/s per process, 30s/RPC, 100 rows/list page and 1,000 history events/page. Runtime floor is `0.1 × (N describes + H history pages + L list pages)` seconds plus network time. Set each shard deadline to at least `2 × RPC_count × (0.1 + measured_p95_RPC_seconds) + 15m`; increase for deep histories. For 25,000 one-page runs and 50ms RPC latency that is about 4.4h/pass with margin, not 2h. Multi-page histories cost more. Budget **two independent passes plus exports/termination**. Do not infer N from the 417 sampled failures.

Round 3 adds validated inclusive/exclusive time bounds and optional complete-history export. Partition StartTime into disjoint, contiguous intervals with about 1,000 executions each. Include an **unbounded first interval**, every later interval through the containment cutoff T, and a tail `StartTime >= T` check expected zero. Count each shard first. Use one inventory process at a time; a second doubles the aggregate read budget and needs explicit DB headroom approval. Example shard (timestamps are illustrative; derive live boundaries):

```sh
./temporal-recovery-inventory --address "$TEMPORAL_ADDRESS" --namespace "$TEMPORAL_NAMESPACE" \
  --started-from 2026-09-18T00:00:00Z --started-before 2026-09-19T00:00:00Z \
  --timeout 1h --history-dir ./private-history/pass-a/day-18 \
  > ./private-inventory/pass-a/day-18.partial
# Only after exit 0 and JSON validation, atomically rename to day-18.json.
```

Record each completed shard's exact bounds/query, capture time, row count and checksum in `shards-a.json`. On timeout/error, no complete JSON manifest is emitted; retained history files are evidence only. Rerun that entire failed shard with a sized deadline/smaller interval. Resume completed shards only within the same containment epoch. Never carry them across a changed registry, producer freeze, image, or business mutation. The tool deliberately never trusts cached history to approve a run.

Repeat every shard independently as pass B (do not reuse A). Merge each pass with a local read-only script; validate ordered interval coverage, unique exact `(workflow_id, run_id)` pairs, namespace/type and nonempty scoped inputs, and recompute C across the union (per-shard subscription counts cannot be summed). Compare A/B exact pairs, statuses and decisions; history event counts can grow from rejected workflow-task retries. Reconcile union counts with a fresh unbounded workflow count/list and every sample pair by authoritative describe, including pairs missing from visibility. Any new/missing/unexplained pair or read failure blocks approval.

`--history-dir` saves the full paginated proto-JSON history with namespace/exact IDs and **full inputs**, mode 0600, atomically per run. The directory must be private (0700). Filenames are hashes of namespace/workflow/run; the file contains the mapping. It exports Running subscription histories; for closed rows and all P/I/R cohorts use exact CLI `workflow show` above and save describes too. Save histories for **every** approved termination, not just manual-reconciliation rows. Verify one valid export/full input per approved pair and transfer evidence plus baselines to the approved encrypted durable backup store. Never commit customer payloads, DB dumps or credentials to either repository.

**Expected/verify:** two matching complete census unions; all approved pairs have full-input/history/describe evidence; backups readable, checksummed and off the incident disk. **Rollback:** delete/retry incomplete local artifacts only; no live change. No termination without the durable evidence receipt.

### 5. WRITE — extend short retention only if required for the approved recovery horizon

**Preconditions:** actual retention read, backup receipt and explicit namespace-admin approval. If retention is shorter than the expected recovery plus audit margin, set the agreed retention (example seven days; use an approved larger value if required):

```sh
t operator namespace update --retention 168h
t operator namespace describe --output json > namespace-retention-after.json
```

**Expected/verify:** exactly one optional namespace setting change, desired retention in describe; zero history resurrected. Export histories continuously regardless of retention. If the operator declines or lacks permission, retain the original value and require verified durable exports for every old/new execution before its current expiry; otherwise stop before termination. **Rollback:** restore the recorded original retention only after recovery/audit exports are complete; reducing it earlier could purge evidence. Never enable archival or alter Temporal SQL mappings ad hoc.

### 6. READ — approve exact dispositions and reconciliation, canonical NONE

**Preconditions:** matching complete inventories and durable backups. `terminate-preactivity` means complete history contains only start and workflow-task scheduled/started/failed/timed-out events, with BAD_SEARCH_ATTRIBUTES evidence. Every other accepted command/signal/marker/activity/child means **manual reconciliation**, never automatic approval.

Produce immutable `termination-approved.json`: namespace, containment cutoff/epoch, approval receipt, counts N/P/I/R, and rows with exact IDs, type, tenant/environment/subscription or invoice mapping, history checksum, `canonical_run: ""`, explicit `decision: "terminate-approved"`, disposition and reviewer. Closed rows get individual terminal dispositions, not a terminate action. Review every member; never silently filter an unsafe row. P/I/R can be zero after approved draining; still reconcile their completed effects. Under the normal step-3 path all N are preactivity. A separately approved stopped-worker variant must resolve every manual row before this step passes.

Cancellation: `CheckCancellationActivity` (`update_billing_period_activities.go:296+`) writes cancellation and calls `PublishCancellationEvents` with fresh event IDs; no already-cancelled guard proves event idempotency. Read effective cancellation, resource termination, children, pending schedules, outbox/delivery; do not re-run billing for already-cancelled subscriptions. Period writes (`UpdateCurrentPeriodActivity`) lack CAS/monotonic protection; compare stored dates to invoices and calculate catch-up from fresh current state. Invoice draft keys are scoped, but `TriggerInvoiceWorkflowActivity` starts fresh invoice processors; map **both** ProcessInvoice/FinalizeDraft and compute/recalculate histories by input invoice ID. Reconcile provider attempts, credits, totals, payment status and uncertain external results before any replay. A single lost activity response is still uncertain even without duplicates.

**Expected/verify:** every old pair accounted for, manual-reconciliation unresolved=0, retain-old=0; sample-only 417 terminate/0 retain is never the live manifest. Per-scoped-subscription business disposition and permitted financial/email effects signed. **Rollback:** revise/reapprove manifests; READ only. Backups do not authorize restoring payment/provider state.

### 7. WRITE — terminate in bounded exact-pair batches with durable restart progress

**Preconditions:** steps 2–6, attributes still unchanged, activity/external effects settled and each exact pair approved. Keep holds active. With the normal path, old workers may still process unrelated work because remaining obsolete runs cannot pass missing attributes. Otherwise use only the separately approved stopped-worker variant.

Use batches of **100 approved pairs**, at most **2 CLI processes concurrently**, each with 45s process timeout; start at most two commands/second. Have a supervisor stop dispatching on first error/load alarm. Never use a wildcard, query termination, latest-run omission or reset. For each pair:

```sh
t workflow describe --workflow-id '<approved ID>' --run-id '<approved run>' --output json > '<private before receipt>.json'
t workflow terminate --workflow-id '<approved ID>' --run-id '<approved run>' \
  --reason 'P10 R3 reviewed obsolete backlog; canonical=none; evidence archived'
t workflow describe --workflow-id '<same ID>' --run-id '<same run>' --output json > '<private after receipt>.json'
```

Assign every manifest row a stable ordinal and checksum. Store one atomic progress record per ordinal: manifest SHA256, exact pair, pre-status, termination result, authoritative post-status and receipt paths. Commit a batch checkpoint only after all 100 rows are described closed; reconcile batch totals before the next batch. This is resumable **by manifest ordinal**, never by skipping the first N rows in a fresh list. At resume, validate manifest checksum and re-describe even completed rows. If the last command timed out/crashed, describe first: Terminated needs no retry; Running requires a fresh activity check and approved exact retry; another closed status needs individual reconciliation. NotFound/permission/unknown output is a stop, never a success. Do not trust a bare CLI exit code or progress marker without authoritative status.

For 25k pairs there are 250 batches. At two command starts/s and three reads/writes per pair, the optimistic lower bound is ~10.4h plus latency/export/checkpoint/reconciliation. Measure the first 100, plan the full hold accordingly, and checkpoint between shifts. Increase concurrency only in a new approved plan with measured Postgres/Temporal headroom; never leave the shared worker stopped for this entire bulk phase by default. CLI/read/write deadlines do not roll back an accepted termination.

**Expected/verify:** N+P+I+R reviewed Running pairs disposed, all receipts exact-ID, all deviations individually reconciled, no Running member left. **Rollback:** termination is irreversible; no restart/reset/resurrection of those runs. Recovery from fresh scoped business state is the forward repair. Stop dispatch, preserve receipts/holds and reconcile on failure.

### 8. READ — zero-old-work gate immediately before short downtime

**Preconditions:** every batch completed and all holds still effective. Repeat unbounded billing/draft inventory with an explicitly sized timeout; expected `running=0`. Re-list **all** open workflows and authoritatively describe every approved pair; P/I/R affected open=0, pending activities=0, delayed starts/timers/cron continuations=0 for the affected cohort. Reconcile visibility with describes and unchanged schedule/caller manifests.

Ensure every managed schedule that either candidate/fallback will create on startup **already exists** with the approved hold state. `EnsureSchedules` preserves existing pause state but creates missing schedules active. Missing entries block startup: separately provision their exact reviewed spec/action **paused** before resuming this plan, with an additional authorization/receipt. Never start the worker and race to pause a newly created schedule. API-created finalization schedules are separate; an absent one is handled by scoped manual finalization below, not automatic provisioning.

**Expected/verify:** zero unknown/old affected Running runs, zero unsettled external operations, compatible registry/free slots unchanged, backups complete, both images staged, stop/start/fallback commands ready. **Rollback:** no new change; remain on old worker with holds and investigate failed gates.

### 9. WRITE — short worker stop and missing-attribute registration

**Preconditions:** step 8 fresh receipt and authorized 30-minute stop window. Stop all pollers of affected queues (including local-mode replicas), record time/replicas and verify task-queue pollers with exact queue names from `internal/types/temporal.go`:

```sh
t task-queue describe --task-queue '<affected exact queue>' --task-queue-type workflow --output json
t task-queue describe --task-queue '<affected exact queue>' --task-queue-type activity --output json
```

If the server rejects enhanced task-queue describe, use the pinned CLI's read-only `--legacy-mode --task-queue-type-legacy workflow` (or `activity`) form and inspect every partition. Poller records can linger; combine this with process/replica evidence and pending-activity descriptions. Termination cannot undo a running activity. If the zero-backlog/settled gate changed, do not register.

While stopped, the split API and Kafka consumer, Postgres/Temporal server and independent Auth remain running under their established pins; approved read APIs and proven-independent ingestion can continue. **All worker-hosted Temporal queues stop**, including grants/expiry/cron/settlement and its in-process webhook router. API/consumer routers may still deliver events unless separately held in step 2. Budget deferred tasks/webhooks/credits and provider-notification storage; no claim that general API health means those services are working. This stop does not cover the hours-long producer hold already approved.

Read attributes again, then create **only** the M names still absent:

```sh
t operator search-attribute create --name SubscriptionID --type Keyword
t operator search-attribute create --name TenantID --type Keyword
t operator search-attribute create --name EnvironmentID --type Keyword
t operator search-attribute create --name FailingActivity --type Keyword
t operator search-attribute create --name FailureReason --type Keyword
```

These are alternatives for missing fields, not an unconditional five-command script. Verify after each response. Do not remove/retype fields, edit SQL aliases, change visibility stores or reset old executions. Final read must show 5/5 Keyword and healthy visibility before starting any poller.

**Expected/verify:** M successful registrations (possibly zero), 5 correct names, old affected Running=0, no activity effects during the stop. **Rollback before first registration:** restart the recorded old image with producer/dunning holds preserved; no old run is recreated. **Rollback after any registration:** leave attributes in place; use the staged bounded containment fallback in step 10, which can start even after a partial registration because its reviewed backport omits the strict guard. Keep zero affected executions and all producer/caller/dunning holds; restore unrelated worker service only. Then diagnose/finish registration in another approved window. If no such fallback was built/tested at step 0, this procedure was a no-go; never substitute an unreviewed old unbounded writer. Attribute deletion is not rollback. At minute 10 begin fallback if candidate readiness fails; by minute 30 either compatible worker service is restored or the incident owner explicitly invokes the preapproved outage escalation. Never silently overrun the budget or restart an unreviewed unbounded writer.

### 10. WRITE — start the approved bounded image, with producers still paused

**Preconditions for billing recovery:** 5/5 registry, zero-old gate, exact image digest/pin approved before step 2. A partial-registration abort may start only the preapproved containment fallback for unrelated work; it may not proceed to step 11. Execute only the recorded worker release/start command; API/consumer pins stay as approved. Verify startup preflight, all worker queues/pollers, no unexpected schedule creation/unpause, healthy unrelated grants/expiry and intended router holds. Optional recovery limits:

```text
FLEXPRICE_TEMPORAL_WORKER_MAX_CONCURRENT_WORKFLOW_TASK_EXECUTION_SIZE=2
FLEXPRICE_TEMPORAL_WORKER_MAX_CONCURRENT_ACTIVITY_EXECUTION_SIZE=1
FLEXPRICE_TEMPORAL_WORKER_WORKER_ACTIVITIES_PER_SECOND=1
```

Do not use workflow-task size/pollers=1 (SDK 1.32.1 panics). These are per-worker/queue throughput limits, **not** per-subscription locks. Keep every billing/draft/finalizer/auto-cancel schedule and all direct/external starts held. Complete this start/fallback within step 9's stop budget.

**Expected/verify:** configured replica count, bounded image SHA/digest, registration preflight passed, S_hold states preserved, zero unsolicited affected runs/collections. **Rollback:** stop faulty candidate, select the staged bounded compatible fallback with holds retained. Keep every registered attribute in place, including a partial set. The containment-only fallback may restore unrelated work after incomplete registration, but never authorize billing. If neither image starts, stop new recovery and invoke outage owner; no old-run reset, database restore or financial reversal is automatic.

### 11. WRITE — recover one scoped subscription, then reconcile invoice terms before collection

**Preconditions:** candidate/fallback healthy, signed per-subscription disposition, auto-cancellation and dunning holds effective. Read fresh scoped state and confirm no Running billing/draft execution for that subscription and no uncertain invoice/provider result. Submit **one** approved normal trigger:

```text
POST /v1/subscriptions/temporal/<subscription_id>/trigger-workflow
```

Use the existing authenticated client with explicit tenant/environment scope; save response workflow/run IDs privately. Already-cancelled subscriptions and existing unprocessed invoice IDs use their separately reviewed scoped disposition, never a blind whole-subscription replay. A failed/uncertain response requires describe/history/business reconciliation before considering another submission.

The **900–1199-second delay (15 to <20m)** in `TriggerInvoiceWorkflowActivity` is a **start delay** for `ProcessInvoiceWorkflow`, not finalization timing. It computes, checks `IsFinalizationDue`, and exits without finalizing when not due. `IsFinalizationDue` (`internal/ee/service/invoice.go:1093–1126`) requires computed draft state, an appropriate computation relative to the period end, and `now > last_computed_at + finalization_delay_seconds`; default delay is **7200s (2h)** (`internal/types/settings.go:525`). With an active 30m finalizer schedule this normally means >2h to the next scan, plus queue/activity delay. With the schedule paused or absent **nothing automatically finalizes later**. Zero-dollar invoices may be SKIPPED; verify the actual status. Tenant configuration may differ, including zero delay and immediate finalization/payment; approve that effect beforehand.

**Late invoices are already overdue by default.** `billing.go:1963–1973` derives due date from historical **period end + payment terms**, default one day (`settings.go:523`). `ComputeInvoice` copies it back to the invoice (`invoice.go:465–541`), so subsequent recompute can undo an earlier adjustment. Auto-cancellation (`subscription.go:5995–6170`) selects finalized pending/failed unpaid invoices after due date + grace (default three days) and immediately cancels; it does not check that the invoice was created late. Leave it paused throughout recovery.

After the **last** compute has finished and no compute/recalculate/daily-draft run can overwrite it, use the existing scoped invoice update path (`router.go:405`, `invoice.go:3689+`, `dto/invoice.go:944+`) for each affected draft or finalized invoice:

```text
PUT /v1/invoices/<invoice_id>
{"due_date":"<operator-approved future RFC3339 payment deadline>"}
```

Choose a deadline **no earlier than the actual customer delivery time plus their agreed full payment terms**, and after the planned finalization/delivery time. Record old/new dates, last-computed timestamp, notification/receipt time and grace deadline; read back the row. This is a separately approved commercial adjustment on the exact invoice, not a subscription period/end-date change. The API accepts draft/finalized invoice due-date updates and rejects past dates. Recompute later means repeat reconciliation/adjustment before any collection resumes. If adjustments are not approved/possible, keep auto-cancel/dunning held until invoice settlement/waiver or another exact signed reconciliation; elapsed historical dates never justify cancelling a late-invoiced customer.

**Expected/verify:** at most one reviewed billing/draft run per scope, fresh correct periods/invoice IDs, all delayed invoice starts accounted for, every payable catch-up invoice has agreed future due/grace or explicit settlement/hold. **Rollback:** stop additional submissions, retain all holds, reconcile persisted periods/events/provider results. Do not undo period/end dates, replay a paid invoice, fabricate a successful payment or replace an uncertain provider idempotency key. Due-date rollback requires commercial approval; restoring the original past date before lifting auto-cancel would recreate the hazard.

### 12. WRITE — scoped finalization only when due; READ verification before the next scope

**Preconditions:** step 11 invoice terms/compute stable, exact invoice approved, no competing ProcessInvoice/FinalizeDraft run and no uncertain charge. Keep the **global finalization schedule paused**. At `last_computed_at + configured delay` (strictly after, with actual predicate verified), start one scoped finalizer through the normal route:

```text
POST /v1/invoices/temporal/<invoice_id>/finalize
```

This path finalizes then attempts payment (`FinalizeDraftInvoiceWorkflow`, `invoice_activities.go:119+`); it is a financial WRITE, not a harmless status check. Approve collection mode, wallet/card/provider effects and any resulting integration notifications. A manual `POST /invoices/:id/finalize` has different orchestration; do not substitute it. Do not trigger the all-tenant schedule to test one customer. If the predicate still says not due, record it and wait with the worker running; do not lower the global delay. If an invoice was already finalized/paid/skipped, use its reconciled disposition and do not submit another finalizer.

READ its complete history and persisted invoice/payment/event results. Expected: no duplicate invoice per scoped period/reason; correct totals/credits/taxes, monotonic valid periods, intended cancellation/resources and one reconciled event sequence; at most one processor/charge for the exact invoice; no nonpayment cancellation caused by late recovery. Verify customer delivery and future payment/grace dates again, including downstream provider/dunning copies. A workflow reporting success can mean skipped finalization or a handled payment outcome; verify business/provider truth. Resolve all Running/delayed/retrying operations and unknown payment/event outcomes before the next subscription. Include retained failed/closed outage-era runs; missing history needs explicit alternate evidence.

**Expected/verify:** one completed reconciled scope and a signed receipt listing all invoice/payment/event IDs and adjusted terms; no open uncertainty. **Rollback:** stop new scopes, keep auto-cancel/dunning/global producers held; individual financial/event repair requires a separate approval. Termination/reset is not a refund and cannot undo notification/cancellation effects.

### 13. WRITE — restore approved producers only after the full cohort passes

**Preconditions:** all N/P/I/R dispositions and C scope receipts complete, no old Running run, no overdue unreconciled recovery invoice, no uncertain provider result, monitoring healthy. Restore normal tested throughput first, with an approved bounded worker restart if required; verify health and unchanged pause states. Recount subscriptions newly due during the hold and review pending DB plan-change schedules; control their first catch-up rather than allowing overlapping scans blindly.

Restore **only previously active, explicitly approved** schedules/callers. Use exact IDs and preserved specs; do not resurrect duplicate legacy schedules simply because they once existed. An absent draft-finalizer is an explicit operator decision: continue scoped manual finalization, or separately authorize one reviewed schedule; do not promise unattended finalization without one. Resume billing/draft/finalizer/integration work in observed stages, retaining cohort dunning and auto-cancel holds until each catch-up invoice is settled/waived or its reconciled future due+grace window and downstream terms are verified:

```sh
t schedule toggle --schedule-id '<approved previously-active ID>' --unpause --reason 'P10 R3 reconciled cohort; staged restoration'
t schedule describe --schedule-id '<same ID>' --output json > '<private restored receipt>.json'
```

For namespace-wide auto-cancellation, **every** affected invoice/subscription must be safe before restoring the schedule; it has no scoped exclusion. Previously paused schedules stay paused. Review buffered provider notifications/system events and pending immediate retries before releasing their holds. Preserve customer payment terms on release; do not process a wave of historical nonpayment cancellations or stale dunning. Record any intentionally retired producer as a separately approved change.

**Expected/verify:** restored count equals approved previously-active count, all other holds have owners/expiry, no unexpected finalizer/cancellation/payment starts, first scans reconcile without duplicate period work. **Rollback:** re-pause the exact newly restored schedules, reapply caller/dunning holds and reconcile already-accepted activities. Unpause/pause cannot reverse effects already sent.

### 14. READ — observe subsequent cycles, evidence retention and Airtel cutoff

**Preconditions:** staged restoration. Observe several 2m billing scans, the actual 30m finalizer/15m cancellation cycles and customer-specific finalization/grace horizon. Verify zero BadSearchAttributes/visibility-write errors, healthy Postgres disk/WAL/load and Temporal backlog, correct schedules/pollers, no duplicate Running scope/invoice pairs, correct credits/settlement/webhook delivery and **no cancellation for outage-created overdue invoices**. Fresh-ID scans still permit future overlap; recontain if seen and require a separate per-subscription/idempotency fix before unattended billing.

Continuously export new failed/uncertain/closed recovery histories and scoped business receipts before actual retention expiry. Restore any temporary retention only after durable evidence review. Missing historical data is documented, not silently reconstructed.

Airtel remains `subs_01M4TJ8X5BVJPWXFHCB4JX6DMY`, environment `env_01M0A3XZCR4VXCJ7NDFC92H1FE`, customer `cust_01M4TJ8QDSH89448T4J80KCFPR`, cutoff **2026-10-27 12:02:17.567 UTC**. Verify intended effective cancellation/no renewal or charge and independent Auth expiry; do not change customer dates for a smoke. Keep [P8's existing commercial/escalation decision](https://github.com/BHM-Ailabs/Plaqad-Portfolio/blob/main/output/incident-followup-2026-10-18/p8-expiring-items.md#bsp-automatic-cancellation-and-the-outage); a still-active BSP row does not extend Auth access. Extension/conversion/manual cancellation or financial adjustment remains separately authorized.

**Expected/verify:** complete release/financial/audit receipts, no unexplained error/duplicate/cancellation, customer terms honored. **Rollback:** READ only; any alarm invokes step 13 containment plus exact scoped reconciliation, without removing attributes or restoring whole databases over external payment effects.
