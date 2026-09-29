# Red–green proofs: the receiver tier (gate R3)

Behavior proved: the chart's receiver tier (#109, `docs/plans/2026-09-28-receiver-tier.md`) — a
real Alloy rendered at pod start by `shepherd receiver render` — forwards each tenant's data
carrying exactly the tenant its gateway route injected, accepts traffic only from the gateway, and
refuses to start on a config the renderer rejects. Cited by `e2e/k8s/receiver_tenancy_test.go`.

Each red run removes exactly one control and records the failure, so no assertion can be green
while the thing it claims is absent.

---

## 1. Pass-through tenancy needs the batch processor's `metadata_keys` (D10)

**Setup (local, real Alloy, 2026-09-28).** The Shepherd image's Alloy (v1.19.2) running a config
rendered by `shepherd receiver render` from a one-exporter pass-through file, exporting OTLP/HTTP
traces to `mendhak/http-https-echo:32` (the same sink the kind test uses, `LOG_WITHOUT_NEWLINE=true`,
`ECHO_BACK_TO_CLIENT=false`); two POSTs of one span each, `X-Scope-OrgID: acme` and `globex`.

**Green** — as rendered:

```
POST acme -> 200
POST globex -> 200
sink: /v1/traces acme
sink: /v1/traces globex
alloy errors: 0
```

**Red** — the same rendered file with the single `metadata_keys = ["x-scope-orgid"]` line removed:

```
POST acme -> 200
POST globex -> 200
sink: /v1/traces ''          <- ONE request, no tenant header, both tenants' spans merged
alloy errors: 0
```

Nothing errors anywhere: both clients get 200 and Alloy logs no error. The batch processor merges
both tenants into one batch and the exporter's `from_context` finds no key, so it OMITS the header.
Against a strictly multi-tenant backend that is an outage; against one with a default tenant, a
silent cross-tenant merge. The kind test's first assessment fails on exactly this — it requires an
export for every tenant and none without a tenant.

## 2. Gateway-only ingress (NetworkPolicy)

In-test control, every run (`receiver_tenancy_test.go`, third assessment): a pod outside the
gateway namespace POSTs straight to the receiver Service three times and must fail to connect
(`000`); the test then deletes the receiver's NetworkPolicy and the SAME request must succeed
(HTTP 200) — so the refusal is the policy, not a broken probe. The policy is restored with
`helm upgrade --reuse-values` before the next assessment.

## 3. A refused config never starts Alloy

The fourth assessment upgrades the release with `receiver.batch.sendBatchMaxSize=100` (below the
batch size — `receiver.Validate` refuses it, and Alloy itself would refuse to start) and requires the
new pod's `render` init container to exit non-zero with a message naming `send_batch_max_size`.

## 4. `send_batch_max_size` must be explicit

Found while building the chart (PR #170): omitted, Alloy applies its own non-zero default cap and
refuses to START a receiver whose `send_batch_size` exceeds it — `alloy validate` accepts that
config. Red: the old rendering failed its readiness probe with one error; green: the fixed one is
ready and healthy with none. Pinned by `internal/receiver/batch_test.go`.

## Kind-suite runs

**Green — CI `e2e-k8s`, run 36479921906 (PR #171, `c43b5cb`, 2026-09-28).** Calico enforcing,
NGF gateway, the chart's receiver built from this tree:

```
--- PASS: TestReceiverPassThroughTenancy (194.60s)
    --- PASS: CNI_enforces_NetworkPolicy (phases 1-3)
    --- PASS: the_gateway's_namespace_can_reach_the_receiver_directly_(the_NetworkPolicy's_allowed_side)
    --- PASS: each_tenant's_spans_reach_the_backend_carrying_exactly_that_tenant,_through_the_real_receiver
          backend saw, by tenant: map[acme:1 direct-probe:1 globex:1]
    --- PASS: a_tenant_header_the_client_sets_itself_never_reaches_the_backend
    --- PASS: the_gateway_is_the_only_ingress:_a_pod_elsewhere_cannot_reach_the_receiver,_until_the_NetworkPolicy_is_removed
          control: without the NetworkPolicy the same direct request succeeds (HTTP 200)
    --- PASS: a_receiver_config_the_renderer_refuses_stops_the_new_pod_at_init,_and_the_running_receiver_keeps_serving
```

(`direct-probe` is the test's own direct-to-receiver requests, tagged so they cannot read as a
dropped tenant.) The render-guard run also showed the blast-radius property now asserted: the new
pod sat at `Init:CrashLoopBackOff` while the previous receiver pod stayed Ready.

**Earlier red runs on this PR were test-harness defects, not product ones**, kept here because they
cost three CI cycles: `utils.RunCommand` (gexe) re-tokenises a command string, mangling a JSON
body, an `sh -c` script and a `jsonpath` expression in turn — the receiver itself was Running and
Ready in every one. Every kubectl call in the test now runs from an explicit argv.

## `gatewayFrom` granularity — decided 2026-09-29: a podSelector is required

The first green run's positive-control probe showed that ANY pod in the gateway's namespace — not
only the gateway's data plane — could reach a pass-through receiver and assert any tenant when
`gatewayFrom` selected by namespace alone. The chart now refuses a `gatewayFrom` peer without a
non-empty `podSelector` (red: the two chart specs pinning it fail with the rule removed), and the
kind test's second assessment asserts the property directly: a non-gateway pod in the gateway's own
namespace is refused.
