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

Recorded once CI's `e2e-k8s` job has run `TestReceiverPassThroughTenancy` on this PR.
