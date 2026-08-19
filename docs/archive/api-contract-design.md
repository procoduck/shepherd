# Management API contract — protobuf/Connect refactor design

> Status: approved for implementation 2026-08-18. Companion progress tracking in
> `docs/project-status.md`. This doc is the authoritative reference for the
> `mgmt-api-contract` implementation workflow.

## Problem

The `/api` management surface is hand-written twice: chi handlers marshal ad-hoc Go
structs, and `web/src/api/client.ts` re-declares every shape by hand. Nothing checks
they agree, and drift has repeatedly produced runtime bugs (most recently R2-C3: the
detail page TypeScript-cast fields the wire never carried). The agent protocol does
not have this problem — it is proto-generated (`collector.v1` via Connect). This
refactor gives the management API the same property.

## Decision

**Connect RPC becomes the single typed contract for the management API.** The stack
already uses Connect (`connectrpc.com/connect`, buf v2, local protoc plugins), so no
new protocol technology is introduced.

- New proto package **`shepherd.mgmt.v1`** in `proto/shepherd/mgmt/v1/*.proto`
  (same buf module rooted at `proto/`).
- **Go server**: generated into `gen/` by the existing `protoc_builtin: go` +
  `connect-go` plugins. Service implementations live in `internal/mgmtapi/`.
- **TypeScript client**: `@bufbuild/protoc-gen-es` v2 (local plugin from
  `web/node_modules/.bin/`, wired into `buf.gen.yaml`) generates message types +
  service descriptors into **`web/src/gen/`** (gitignored? NO — committed, same as
  `gen/`, so builds stay hermetic). Frontend calls go through
  `@connectrpc/connect` + `@connectrpc/connect-web` v2 `createClient`.
- **External integrations keep plain HTTPS JSON**: the existing REST routes remain
  mounted at their current paths, but every chi handler becomes a **thin shim** that
  builds the proto request, calls the same Connect service implementation, and
  marshals the proto response through `protojson` with `UseProtoNames: true` so the
  legacy snake_case JSON, `{"items":[...],"total":n}` lists, and
  `{"error":{"code","message"}}` envelope are byte-compatible. One implementation,
  two wire dialects. Additionally the Connect endpoints themselves are plain
  HTTP POST + JSON (the Connect unary protocol), so integrators may also call them
  directly; the REST shims exist for backward compatibility and ergonomics.

## Services (all under `shepherd.mgmt.v1`)

| Service | Methods (mirroring current routes) | Authz |
|---|---|---|
| `MeService` | GetMe | any session |
| `AdminService` | ListOrgs, CreateOrg, UpdateOrg, DeleteOrg, ListClusters, ClaimCluster, UnclaimCluster, ListAgentTokens, CreateAgentToken, RevokeAgentToken, SearchGroups | app admin (SearchGroups: app admin or org admin) |
| `FleetService` | ListCollectors, GetCollector, GetServedConfig, CreateAssignment, DeleteAssignment, ListAttributes | org reader / orgadmin for writes |
| `PipelineService` | ListPipelines, GetPipeline, CreatePipeline, UpdatePipeline, DeletePipeline, EnablePipeline, DisablePipeline, ValidatePipeline, PreviewMatches, ListRevisions | reader for reads, orgadmin for writes |
| `DestinationService` | List/Get/Create/Update/Delete | reader read, orgadmin write |
| `GitOpsService` | ListCredentials, CreateCredential, DeleteCredential, ListRepoLinks, CreateRepoLink, DeleteRepoLink | orgadmin |
| `WizardService` | ListWizards, GetWizardSchema, CommitWizard | orgadmin |
| `VisualService` | Render, Validate, UpgradeCheck, GraphView | orgadmin (GraphView: reader) |
| `SimulateService` | SimulateRelabel, SimulateLogs | orgadmin |
| `AuditService` | ListAudit | orgadmin |

**Out of contract, unchanged**: `/auth/*` (browser redirect flows + local login),
`/api/schema/current` and `/api/schema/{version}` (large dynamic JSON artifact with
ETag caching — stays REST), `/metrics`, and the agent protocol (`collector.v1`).

## Contract modeling rules

- Org scoping: every org-scoped request message carries `string org_id = 1`. A
  shared Go interface (`interface{ GetOrgId() string }`) lets one authz interceptor
  resolve the org.
- Enum-like fields (`role`, `source`, `auth_mode`, `remote_config_status`, wizard
  field `type`) stay **strings** in proto, validated server-side — this keeps the
  shim JSON byte-identical to today (proto enums would mangle values).
- Timestamps: `google.protobuf.Timestamp`; protojson renders RFC3339 which matches
  the legacy strings.
- `local_attributes` / `matchers` / `wizard_state` / graph documents:
  `google.protobuf.Struct` (or `ListValue`) where the payload is genuinely dynamic;
  typed messages everywhere the shape is known (matchers are `repeated string`).
- Lists: each `List*Response` has `repeated <T> items = 1; int32 total = 2;`
  requests carry `int32 limit`/`int32 offset` where the REST route supported them.
- Errors: services return `connect.NewError` with standard codes
  (InvalidArgument, NotFound, PermissionDenied, Unauthenticated, AlreadyExists,
  FailedPrecondition for 422-class validation failures, Internal). Validation
  diagnostics ride in a `Diagnostics` message on the response (validate endpoints
  return 200 + diagnostics today — keep that shape). The REST shim maps
  connect codes → today's HTTP statuses + envelope
  (InvalidArgument→400, Unauthenticated→401, PermissionDenied→403, NotFound→404,
  AlreadyExists→409, FailedPrecondition→422, Unavailable→503, else 500).

## Server wiring

- Connect handlers are mounted inside the same authenticated router group as
  `/api` (session middleware + CSRF already applied): each
  `mgmtv1connect.New<X>ServiceHandler(impl, connect.WithInterceptors(authz))`
  is `r.Mount`ed at its returned path prefix (`/shepherd.mgmt.v1.<X>Service/`).
  The server-level guards (`apiGuard`, SPA fallthrough) must 404 unknown
  `/shepherd.mgmt.v1.*` paths instead of serving the SPA.
- **Authz interceptor**: one `connect.UnaryInterceptorFunc` with a
  procedure→requirement map (`app-admin` | `org-admin` | `org-reader` | `any`),
  reusing the exact role logic of `auth.RequireAppAdmin`/`auth.RequireOrgAccess`
  (extract shared helpers rather than duplicating; session comes from
  `auth.SessionFromCtx`).
- **CSRF**: the web transport adds `X-Requested-With: XMLHttpRequest` via a
  Connect interceptor; the server keeps enforcing it for cookie-session requests
  on mutating calls (all Connect calls are POST — enforce on all Connect paths).
- Handlers move their business logic INTO the service impl; the chi handler file
  shrinks to param-parsing + service call + protojson render. No business logic
  may remain in a shim.

## Frontend rules

- `web/src/api/client.ts` hand-written types are deleted per resource as each
  service migrates; pages import generated types from `web/src/gen/` and call
  `createClient(<Service>, transport)`. One shared transport in
  `web/src/api/transport.ts` (`createConnectTransport({ baseUrl: '/', fetch:
  same-origin credentials })` + CSRF header interceptor + an error-normalizing
  interceptor so pages keep a uniform error object).
- MSW test mocks (`web/tests/mocks/`) intercept the Connect POST paths and answer
  with Connect JSON; the seed router maps procedures instead of REST paths.
- TanStack Query keys stay stable (`['collectors', orgId]` etc.) — only the
  fetcher changes.

## Testing & migration safety

- The existing Ginkgo mgmtapi suite, `make e2e`, and the fullstack Playwright
  suite all exercise the **legacy REST paths** — they are the regression harness
  proving the shims are faithful. They must pass unchanged (except where they
  assert shapes that were already buggy).
- Each migrated service adds Connect-path Ginkgo coverage (happy path + one authz
  denial + one error mapping case per service minimum).
- Vitest covers the transport interceptors; Playwright specs keep passing against
  the MSW Connect mocks.

## Non-goals

- No streaming methods, no gRPC-Web compatibility work, no OpenAPI generation,
  no API-token auth for the management API (external callers authenticate exactly
  as today), no removal of the REST surface (it is now a supported shim), no
  `/api/schema` migration.
