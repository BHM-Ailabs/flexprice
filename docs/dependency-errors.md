# Dependency failure HTTP contract

Database errors preserve their underlying cause from repository lookup or
transaction begin through the HTTP error handler. A contextual `ErrNotFound` or
`ErrValidation` marker is not evidence that a database operation completed.

For an error marked `ErrDatabase`, `ResolveError` applies this policy:

- Typed infrastructure failures return **503** with **`Retry-After: 5`** and a
  sanitized temporary-unavailability response. This includes `*net.OpError`,
  `*net.DNSError`, unreachable/refused/reset/timed-out connections, bad/closed
  connections, EOF, context cancellation/deadline, PostgreSQL class **08**, all of
  class **53** (insufficient resources, including disk/memory/connection limits),
  all of class **58** (system errors), and **57P01–57P03** (shutdown/recovery).
- PostgreSQL **57014** (`query_canceled`) returns **503**. lib/pq can return it
  when a context deadline fires during a query, without a `DeadlineExceeded`
  cause. Explicit server cancellation has the same incomplete-request outcome.
  This response does not establish whether an earlier financial write committed.
- PostgreSQL **XX000** remains **500** unless another typed infrastructure cause
  is present. An internal server error alone can be a persistent bug and does
  not prove temporary unavailability. Unknown database errors also remain **500**.
  Neither case may fall through to a contextual 404 or 400.
- Domain markers may produce 404/400/409/403 only when the chain contains a
  deterministic cause: Ent not-found, validation or constraint errors,
  `sql.ErrNoRows`, PostgreSQL class **22**/**23** (data/constraint errors), or
  the private query-validation cause emitted by pure DSL field resolution.
  Deterministic causes without a domain marker retain the database-error 500.
  Ordinary domain errors without an `ErrDatabase` marker keep their existing
  status. Genuine API-key denials remain **401**.

The generated Ent package imports `internal/types`, which imports
`internal/errors`. To avoid that import cycle, deterministic Ent detection in
`internal/errors` checks concrete package/type identity while walking the error
chain. Tests in the external `errors_test` package exercise the real generated
Ent types. No message matching or generated-code edits are involved.

DSL filter/sort field resolvers consult local column definitions before SQL.
Their validation errors retain a distinct private cause through repository and
service wrappers. `WithQueryValidation` is restricted to this pure construction
boundary: it refuses database-marked errors, infrastructure failures and errors
without a validation marker. Ordinary contextual validation/not-found markers
still cannot override an unknown database failure. Infrastructure precedence
remains first even when an error chain contains query-validation evidence.

Repository translations of proven missing rows must preserve the Ent absence
cause, or `sql.ErrNoRows` for a successful zero-row update/delete. Replacing that
cause with a new domain-only message loses the proof needed by outer wrappers.

Services must check `ierr.IsNotFound` before translating a repository error to
`ErrNotFound`, especially when constructing a replacement error that would lose
the cause. A real lookup outage must never become an empty subscription result.
Dependency diagnostics retain safe reason categories and validated five-character
SQLSTATE codes; driver messages, queries, DSNs and Detail fields are excluded.

Consumers must distinguish absence (404) from dependency failure (503). Plaqad
Auth currently converts customer/subscription/entitlement 404s to absent data and
rethrows 503s. Its client does not preserve `Retry-After` or automatically retry
503. Its outbox uses its own bounded backoff, and some Auth route error handlers
still turn an upstream 503 into a generic 500. That consumer follow-up is outside
this BSP change. Never introduce automatic non-idempotent financial retries from
the presence of a 503 header alone.
