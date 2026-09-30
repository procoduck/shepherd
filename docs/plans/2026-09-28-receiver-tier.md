# Receiver tier (#109) — implementation plan (2026-09-28)

Builds the receiver tier the gateway plan's W4 still lacks, to the bar gate **R3** sets
(`docs/gateway-tier-plan.md` §7), then brings R3 back for sign-off. Until R3 signs, the receiver
stays **off by default** (`receiver.enabled: false`) — gateway plan §5 forbids enabling it sooner.

## 0. Where this starts

- **The renderer exists and is not wired.** `internal/receiver` renders an OTLP receiver config
  (`Render`) and refuses unsafe ones (`Validate`): D10 pass-through tenancy wires `include_metadata`
  on every listener and one `otelcol.auth.headers` `from_context` onto every exporter; a gRPC
  listener with pass-through is refused (an HTTPRoute cannot front gRPC, so the tenant would be
  client-asserted); unbounded body/message sizes are refused; a hardcoded tenant header in a
  pass-through pipeline is refused; the batch processor's `metadata_keys` defect from the
  2026-08-22 review is fixed and pinned. Nothing imports the package.
- **The chart deploys no receiver.** The simulator is the pattern to copy: its own Deployment,
  Service, NetworkPolicy, ServiceAccount and Secret behind `simulator.enabled`, with a kind test
  proving `enabled=false` removes it entirely and Layer-B probes proving containment from inside
  the pod (`e2e/k8s/simulator_containment_test.go`).
- **Tenant routes are stored but never applied.** `gateway.ApplyRoute` is built and kind-proven
  (`e2e/k8s/route_apply_test.go`) but has no production caller. That wiring is **out of scope**
  here (decision 3); this plan's e2e applies its route directly, as `route_apply_test.go` does.

## 1. Decisions (taken 2026-09-28)

1. **Config is rendered at pod start, not served.** An init container runs a new
   `shepherd receiver render` subcommand — `internal/receiver`'s `Validate` + `Render` over a
   config file mounted from the chart — into an `emptyDir`; the Alloy container runs that file.
   The receiver is platform infrastructure serving many tenants, not one org's collector, so the
   remotecfg path (org-scoped collector identity, a new serve path) is not needed for R3. A config
   change is a `helm upgrade`. Served config for the receiver can come later if a need appears.
2. **Destinations come from chart values.** Tenant-aware platform backends (Mimir / Loki / Tempo),
   one exporter per signal: endpoint plus an optional `Authorization` secret by reference. Org
   `Destination` rows stay per-org and are not used by the shared receiver.
3. **Wiring tenant-route apply (`CreateTenantRoute` → `ApplyRoute`) is a separate issue**, opened
   after R3 signs. Without it a tenant route stored in Shepherd still delivers nothing — the docs
   say so until that lands.

## 2. What gets built

### 2.1 `shepherd receiver render` (Go)

- Input: a YAML file whose shape is `receiver.Config` (OTLP pipelines only for this build; Faro stays
  demand-driven per D10). Output: the rendered Alloy config at `--out`.
- Runs `receiver.Validate` first and exits non-zero with its message — a bad values file fails the
  pod at init, visibly, instead of starting an Alloy that forwards untagged data.
- Secrets never pass through the file: exporter credentials are `SecretHeaderEnv` names, resolved
  by Alloy's `sys.env` from a Kubernetes Secret mounted as env on the Alloy container only.

### 2.2 Chart (`deploy/helm/shepherd`)

| Template | Content |
|---|---|
| `configmap-receiver.yaml` | the `receiver.Config` YAML built from `receiver.*` values |
| `deployment-receiver.yaml` | init container (Shepherd image, `receiver render`) → `emptyDir` → Alloy container (`ALLOY_IMAGE` from `versions.env`, `alloy run`); non-root, read-only rootfs, dropped capabilities, `automountServiceAccountToken: false`, resource requests/limits |
| `service-receiver.yaml` | ClusterIP, OTLP/HTTP port only (no gRPC — pass-through refuses it) |
| `networkpolicy-receiver.yaml` | **Ingress:** only from the gateway's namespace/pods (`receiver.networkPolicy.gatewaySelector`), on the OTLP port. **Egress:** DNS plus the configured destinations only. Default-deny otherwise |
| `serviceaccount-receiver.yaml` | dedicated, no RBAC |

Values: `receiver.enabled` (**default `false`**), `receiver.mode` (`pass_through` default; `static`
allowed), listener size limits (required, no implicit defaults), batch settings, per-signal
exporters, `receiver.networkPolicy.gatewaySelector`, image, resources. `NOTES.txt` states the
receiver is off until R3 signs, and that the prefix is an identifier, not an authorizer (§3 of the
gateway plan). `values.schema.json` / repocheck updated to pin the default-off and the no-gRPC
listener.

### 2.3 What R3 is shown (tests, red-first)

1. **Real-Alloy pass-through tenancy, end to end** (kind suite, new
   `e2e/k8s/receiver_tenancy_test.go`): client → NGF gateway → HTTPRoute (prefix strip +
   `X-Scope-OrgID` injection, applied by the test) → **the chart's receiver Alloy** → a sink that
   records each request's `X-Scope-OrgID`. Two tenants' routes; assert each tenant's OTLP data
   arrives carrying exactly its own tenant, and nothing arrives untagged. Red run: rendering the
   receiver with the batch processor's `metadata_keys` removed must fail this test — the defect
   every static gate missed.
2. **The gateway is the only ingress** (Calico-enforced, like the simulator's Layer-B probes): a
   pod outside the gateway namespace cannot reach the receiver Service; the gateway can. Red run:
   the probe must fail with the NetworkPolicy removed.
3. **Client-asserted tenancy is stripped or impossible**: a request that arrives through the
   gateway carrying its own `X-Scope-OrgID` lands under the route's tenant, not the client's.
4. **Off-switch, tested**: `receiver.enabled=false` (the default) renders no receiver object at
   all — the same shape as the simulator's "removes it entirely" test, plus a `helm template`
   assertion that defaults produce nothing.
5. **Render guard**: `shepherd receiver render` exits non-zero on a config `Validate` refuses, and
   the init container failure is what an operator sees (pod `Init:Error`), asserted in kind.

## 3. Phasing (one PR per phase, each green before the next)

| PR | Scope | Proof |
|---|---|---|
| 1 | `shepherd receiver render` subcommand + tests | unit tests: valid config renders, each `Validate` refusal exits non-zero with its message |
| 2 | Chart templates + values + schema + repocheck, default off | `helm lint`, template goldens, repocheck; kind: off-switch test (2.3 #4) |
| 3 | Kind e2e: tenancy (2.3 #1, #3), NetworkPolicy (2.3 #2), render guard (2.3 #5) | each red-run recorded in `docs/proofs/receiver-tier.md` |
| 4 | Docs (receiver page, routes caveat) + the R3 sign-off packet in the gateway plan §7 | `make check-docs-drift`; R3 brought to the user |

Only after R3 is signed: a follow-up issue to wire tenant-route apply, and a separate decision on
whether the receiver defaults on.

## 4. Risks and non-goals

- **Kind-suite cost.** A second Alloy plus a sink adds minutes to `e2e-k8s`, which is path-filtered
  and weekly — acceptable; the tenancy proof is the reason R3 exists.
- **Destinations reachable from kind.** The sink stands in for Mimir in the e2e; the NetworkPolicy's
  egress allowlist is proven against it, not against a real Mimir.
- **Non-goals:** Faro (D10, demand-driven), gRPC ingress, served/remotecfg receiver config,
  tenant-route apply, turning the receiver on by default, horizontal autoscaling of the receiver.
