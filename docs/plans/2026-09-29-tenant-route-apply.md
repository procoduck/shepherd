# Tenant-route apply — implementation plan (2026-09-29)

The receiver tier's follow-up (#109 closed, R3 signed 2026-09-29). Today a tenant route created in
Shepherd is only a database row: nothing puts its HTTPRoute in the cluster, so it delivers no
traffic, and the Receiver tier docs tell operators to write the route by hand. This plan makes
Shepherd apply, update and remove those HTTPRoutes itself, using the already-built and kind-proven
`gateway.ApplyRoute` (`internal/gateway/apply.go`), which has had no production caller.

## 0. Where this starts

- `TenantRouteService` (`internal/mgmtapi/rpc_tenant_route.go`): Create mints a segment and stores a
  row (tenant from the org, gateway name/namespace from the request); Rotate marks the current row
  `deprecated` with a `valid_until` overlap and creates a new active one; Revoke sets `revoked`.
- `gateway.ApplyRoute(ctx, Applier, RouteSpec, ApplyOptions)` renders the HTTPRoute, checks the
  installed Gateway API CRD version (D3), applies, and polls `status.parents[]` until the route is
  verifiably attached — a refusal (`allowedRoutes` does not admit the namespace) or a deadline is an
  error, never an assumed success. `Applier` is implemented only in the kind tests.
- The server has no Kubernetes client, the chart grants Shepherd's ServiceAccount no RBAC, and the
  server config does not know its own namespace or the receiver's Service.
- The janitor sweep that turns an expired `deprecated` route into `revoked` was never built.

## 1. Decisions (taken 2026-09-29)

1. **A background reconciler, not the RPC.** Create/Rotate/Revoke keep changing only the database.
   A reconciler loop computes each route's desired state, applies or deletes its HTTPRoute, retries
   failures, and records the outcome on the row. No request blocks on the minutes attachment can
   take; a restart resumes where it left off.
2. **`k8s.io/client-go` becomes a direct dependency** (already in the module graph indirectly). The
   production `Applier` uses its dynamic client for HTTPRoutes and for the one CRD read D3 needs —
   no controller-runtime.
3. **Additive proto fields** on `TenantRoute`: `apply_status`, `apply_message`, `applied_at`. No
   renames or removals.

## 2. Desired state and what the reconciler does

| Route row | HTTPRoute in the cluster |
|---|---|
| `active` | present, applied and **verified attached** |
| `deprecated`, `valid_until` in the future | present (the rotation overlap) |
| `deprecated`, `valid_until` passed | absent — and the row becomes `revoked` (the missing janitor) |
| `revoked` | absent |
| kind `faro` | not applied: no Faro receiver is deployed (D10) — status says so |

Each pass lists routes, applies the present ones through `gateway.ApplyRoute` (idempotent: it
converges an existing object), deletes the absent ones, and deletes **orphans** — HTTPRoutes carrying
Shepherd's managed-by label whose row no longer wants them. Objects are named from the route id
(stable across restarts) and labelled with it; the route lives in Shepherd's own namespace, next to
the receiver Service its `backendRef` names (no ReferenceGrant needed). The Gateway is never
created or modified, in either D8 ownership mode — only the HTTPRoute.

**Apply status** per route: `pending` (not yet attempted, or changed since), `applied` (attachment
verified), `refused` (the gateway refused attachment — its reason in `apply_message`), `error`
(anything else — the error in `apply_message`, retried with backoff), `not_applicable` (Faro, or
apply disabled). `applied_at` is the last verified attachment.

## 3. What gets built

- **Migration** `00NN_tenant_route_apply_status`: `apply_status` (default `pending`, CHECK-listed),
  `apply_message`, `applied_at`. sqlc queries for the reconciler's reads and status writes, and one
  that flips expired `deprecated` rows to `revoked`.
- **Proto:** the three fields on `TenantRoute`; `make generate`.
- **`internal/gateway`:** a client-go-backed `Applier` (dynamic client; Apply = create-or-update,
  Get, and the CRD annotation read), plus `Delete`.
- **Reconciler** (new, beside `internal/gitsync`'s loop): interval + jittered backoff per route,
  started by `internal/server` only when route apply is enabled.
- **Config:** `gateway.routes.apply.enabled`, the namespace routes live in, and the backend
  (receiver Service name and port).
- **Chart:** when `receiver.enabled` (and `receiver.applyTenantRoutes`, default true): a namespaced
  Role (get/list/watch/create/update/patch/delete on `httproutes`), a ClusterRole limited to `get` on
  the `httproutes.gateway.networking.k8s.io` CRD, their bindings to Shepherd's ServiceAccount, the
  namespace via the downward API, and the receiver backend in the server config. No RBAC at all when
  the receiver is off.
- **UI:** the Tenant routes page shows each route's apply status and message.
- **Docs:** the Receiver tier page drops "create them yourself for now" in favour of how applied
  routes appear and how a refusal is shown; the gateway plan's §9 ledger and the project ledger.

## 4. Tests (red-first)

1. Reconciler unit specs with a fake `Applier`: each desired-state row above; a refusal records
   `refused` with the gateway's reason; an error retries; an orphan is deleted; an expired deprecated
   row is revoked and its route removed; Faro is `not_applicable`.
2. mgmtapi: Create/Rotate/Revoke still only touch the database; `ListTenantRoutes` returns the apply
   fields.
3. Chart: RBAC and config render only with the receiver on; the Role is namespaced and the
   ClusterRole grants exactly `get` on the one CRD.
4. **kind, end to end:** create a tenant route **through the Connect API** → the reconciler applies
   it → OTLP through the gateway reaches the receiver and the sink with the org's tenant (the full
   loop, no hand-written YAML); rotate → both segments work during the overlap; revoke → the
   HTTPRoute is gone and the path no longer routes; a Gateway that does not admit the namespace →
   `apply_status: refused`.

## 5. Phasing (one PR per phase, each green before the next)

| PR | Scope |
|---|---|
| 1 | Migration + proto fields + store queries; API returns `apply_status: pending`. No behaviour change |
| 2 | client-go `Applier` + reconciler + config, fully unit-tested; server starts it only when enabled |
| 3 | Chart RBAC/config (receiver-gated) + chart specs |
| 4 | UI status column + mocked spec |
| 5 | kind e2e (the full loop above) + docs + changelog |

## 6. Risks and non-goals

- **RBAC surface.** Shepherd gains write access to HTTPRoutes in its own namespace only, and read of
  one CRD — nothing cluster-wide beyond that, and nothing when the receiver is off.
- **Operator-owned Gateways** may refuse our namespace; that is surfaced as `refused`, never retried
  as an error forever.
- **Non-goals:** creating or managing Gateways, cross-namespace backends (ReferenceGrant), Faro
  routes, multi-cluster.
