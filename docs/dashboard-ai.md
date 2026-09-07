# Dashboard AI: pricing previews and billing questions

Date: 2026-09-07. Implementation spans `BHM-Ailabs/flexprice`, `BHM-Ailabs/flexprice-front`, and `ailabs-BHM/flexprice-railway`.

## Product behavior

**Create plan with AI** on Plans opens the existing pricing setup page. Natural-language descriptions now use server-side OpenRouter structured output via `POST /v1/ai/pricing/parse`. The returned schema is validated before rendering a preview. Creation remains a separate, explicit dashboard action. Static templates remain available without a model call. Flat-fee plans can have no features. Existing plan lookup keys are checked before any creation; setup refuses collisions rather than adding new charges to an existing plan. A failed creation can leave partial records because the existing multi-request setup API is not transactional. Review those records before retrying; an ambiguous create is never automatically retried under another key.

**Ask FlexPrice** is available throughout the authenticated main dashboard. The panel keeps conversation state across page navigation and resets it on user, tenant, or environment change. It can answer billing questions and explain illustrative quotes; it cannot send quotes, create subscriptions, issue invoices, collect money, or change catalogue records. History is memory-only and clears on reload/sign-out.

Supported read objects: plans, features, prices, customers, subscriptions, invoices, wallets, entitlements, addons, meters, credit grants, payments, coupons, costsheets, and groups. Revenue uses the same `dashboard/revenue-dashboard` endpoint as the dashboard, with explicit time bounds and currency-separated summaries, collections, aging, and leaderboards. Tool source links and retrieval timestamps accompany successful lookups. Queries are paginated; answers must distinguish a page from an exhaustive list. Credentials, connection records, users, workflow histories, downloads, and mutating GET routes are excluded.

## Architecture and boundaries

- One `OpenRouterService` is injected through FX. Runtime key: `FLEXPRICE_OPENROUTER_API_KEY`. Optional model override: `FLEXPRICE_OPENROUTER_MODEL`; default `google/gemini-2.5-flash`. No key or shared FlexPrice service credential is shipped to the browser.
- Existing Gemini endpoint is retained for older clients. New dashboard builds call the provider-neutral `/ai/pricing/parse` route.
- Standard OpenRouter chat completions are used for tool calling and JSON Schema structured output. The existing Gemini `nullable` shape is converted to standard `anyOf` before sending the pricing schema. Structured output and tool calling are separate requests.
- Authenticated routes require the existing `ai` permission. Every assistant lookup re-enters the same server's fixed loopback `/v1` API with the original caller credential and the authenticated environment. Existing authentication, tenant lifecycle, environment binding, and route permissions are applied again. No model-selected host, arbitrary HTTP method, SQL, or arbitrary API path is accepted. Redirects are refused.
- Tool calls run sequentially. Each chat has a 180-second deadline, at most 8 model rounds and 16 tool calls; reads are limited to 25 records per page and 256 KiB per result. The model response is bounded to 1 MiB and 8,192 output tokens. Pricing has a 100-second deadline. At most 2 requests per tenant/environment and 8 globally run concurrently per API process.
- The assistant accepts only bounded user/assistant text history; clients cannot forge system prompts, tool calls, or tool results. Tool results are described as untrusted data. Secret/token/password/API-key/contact/payment-method fields are removed before model use. Metadata is restricted to selected catalogue/activation/publication fields so activation gates remain visible.
- OpenRouter provider routing requires supported parameters and disallows providers collecting data. Error bodies and model content are not logged by the integration. The model still requires human review; source links make its evidence inspectable.
- A dedicated OpenRouter key was requested. The ambiguous spoken word “queue” was interpreted as “key”; no background task queue or durable bearer-token storage is introduced for this interactive workflow.

## Why direct read tools

FlexPrice already publishes an MCP server, but exposing its full generated mutation surface is unnecessary for an embedded read-only dashboard assistant. A small, audited tool registry limits its scope and uses the live API as the source of truth. Indexing billing objects into a separate vector database would duplicate access rules and introduce stale revenue data. This implementation uses live API reads instead.

## Validation

- Go service tests cover read-path allowlisting, traversal and tenant-override rejection, forged tool messages, nullable-schema translation, caller credential/environment forwarding, redirect refusal, sensitive-field filtering, scope/concurrency limits, and invalid/partial provider responses.
- An opt-in live test calls OpenRouter and queries the production catalogue through the same reader code. It never creates billing objects. Run with `FLEXPRICE_AI_LIVE_SMOKE=1` and the server key plus `FLEXPRICE_API_URL`, `FLEXPRICE_API_KEY`, `FLEXPRICE_ENVIRONMENT_ID`: `go test ./internal/ee/service -run TestOpenRouterLiveSmoke -count=1 -v`.
- Frontend tests validate malformed pricing, negative amounts, feature references, duplicate names/currency codes, all existing template orchestration paths, and refusal of an existing plan key before writes. Run with `VITE_ENVIRONMENT=self-hosted bunx vitest run src/api/ai/validation.test.ts src/api/ai/orchestrator.test.ts`.
- Frontend production build and API compilation pass. The assistant panel was visually inspected in a local browser. The deployed API passed full dashboard pricing-schema validation (Explore $0/month and Pro $29/month with a $0.01 usage price), and a live dated revenue question returned an authenticated source. All 15 supported object-list endpoints returned HTTP 200. A focused generated API contract is included in `dashboard-ai.openapi.json`; the full existing Swagger catalogue has unrelated generation drift and was preserved.
- The required full loglint gate exposed four pre-existing LL006 failures in Paystack/payment-processor logs. Literal error fields were added to those log statements; payment behavior is unchanged.

## Deployment

The dedicated OpenRouter key has a $20 total usage limit and no automatic reset. Set the OpenRouter key only on the Railway API service. Pin the reviewed backend and frontend commits in the Railway Dockerfiles; deploy API before the web build, then verify authenticated `/ai/pricing/parse`, `/ai/assistant`, existing revenue, and the browser flow. No database migration is needed. Rollback uses the previous Docker source pins. Do not place the OpenRouter key in build args or `VITE_*`.

## Primary research sources

- [OpenRouter tool calling](https://openrouter.ai/docs/guides/features/tool-calling): the application executes function calls and sends results back; tools are included on every round.
- [OpenRouter structured outputs](https://openrouter.ai/docs/guides/features/structured-outputs): supported models can constrain responses to JSON Schema.
- [OpenRouter model catalogue](https://openrouter.ai/api/v1/models): verified Gemini 2.5 Flash supports tools and structured output. Its supported-parameter list does not include `parallel_tool_calls`; that flag was removed after a live routing test rejected it.
- [FlexPrice API introduction](https://docs.flexprice.io/api-reference/introduction) and [official MCP server](https://github.com/flexprice/mcp-server): existing API/tool coverage for billing entities.

The checked-out application source and the deployed read APIs remain the authority for this customized FlexPrice instance.
