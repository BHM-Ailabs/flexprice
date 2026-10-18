# Temporal namespace prerequisites and recovery

Subscription billing emits custom search attributes before its first activity.
`workflow.UpsertSearchAttributes` queues a command; it does not synchronously
validate server-side registration. Missing mappings cause workflow-task failures
and leave executions open and retrying. Catching a local SDK error is insufficient.

Before local or `temporal_worker` mode updates schedules or starts polling, the
worker now performs a read-only `OperatorService.ListSearchAttributes` request
against `temporal.namespace`. All five attributes must have type **Keyword**:

- `SubscriptionID`
- `TenantID`
- `EnvironmentID`
- `FailingActivity`
- `FailureReason`

The IDs/activity names and the existing failure-query helpers require exact
matching. `Text` would change the failure-reason query semantics. Existing values
remain strings, and workflow commands and histories are unchanged by this guard.

The startup check has a ten-second deadline. A missing/mistyped mapping or denied
operator read fails startup with an actionable error. Grant only the required
operator read permission to the worker identity; this code cannot create mappings.
API-only and consumer-only startup do not perform this worker prerequisite check.

## Operator-only registration

Use an already authorized private-network shell and CLI. Confirm the server,
namespace ID, visibility store and existing mappings first. For Railway BSP the
observed endpoint is `temporal.railway.internal:7233`, namespace `default`.
Never expose the frontend publicly to perform maintenance.

```sh
temporal --address temporal.railway.internal:7233 operator cluster describe
temporal --address temporal.railway.internal:7233 --namespace default operator namespace describe
temporal --address temporal.railway.internal:7233 --namespace default operator search-attribute list
```

After approval, create **only missing** attributes. These are state-changing
operator actions, not commands run by application startup:

```sh
temporal --address temporal.railway.internal:7233 --namespace default operator search-attribute create --name SubscriptionID --type Keyword
temporal --address temporal.railway.internal:7233 --namespace default operator search-attribute create --name TenantID --type Keyword
temporal --address temporal.railway.internal:7233 --namespace default operator search-attribute create --name EnvironmentID --type Keyword
temporal --address temporal.railway.internal:7233 --namespace default operator search-attribute create --name FailingActivity --type Keyword
temporal --address temporal.railway.internal:7233 --namespace default operator search-attribute create --name FailureReason --type Keyword
```

Do not remove or retype an existing mapping without investigating its users and
history. SQL Visibility supports custom attributes; Elasticsearch is not required
on supported Temporal SQL versions. Verify the actual schema/store if the list
or creation operation reports a visibility error or exhausted attribute slots.

## Recover existing work before adding more

1. Inventory running and failed subscription/invoice executions using built-in
   attributes (`WorkflowType`, `ExecutionStatus`, `WorkflowId`), not the missing
   aliases. Read representative histories and scoped subscription/invoice rows.
2. Plan backlog containment **before registration**: registration can immediately
   release already-running retries and may result in invoices, payments, emails
   and cancellation events. Pausing a schedule prevents new runs, not existing
   executions. An operator-approved worker maintenance window and reduced worker
   concurrency may be needed. Repeated subscription scans currently create
   distinct workflow IDs; do not assume one open run per subscription.
3. Let existing open workflow tasks retry after registration. Inspect the same
   run ID for accepted upsert and activity events, and eventual completion. No
   reset or replacement workflow is required solely for this command rejection.
4. Reconcile by tenant, environment, subscription and billing period. Preserve
   existing finalized/paid invoices and payment idempotency keys. Investigate
   duplicate open executions before bulk catch-up; do not blindly reset or
   terminate them. Closed failures caused by exhausted activity retries require
   a separate, reviewed recovery decision after checking persisted side effects.
5. Verify current periods, due end-date cancellations, invoice computation and
   finalization, and payment/event delivery. Check database load, disk use, task
   backlog and `BadSearchAttributes` counts during the drain. Resume schedules
   only after reconciliation. A successful worker startup is not billing proof.

This PR is a preventive startup guard. It neither repairs live registrations nor
replays workflows, changes billing records, or changes a Railway source pin.
