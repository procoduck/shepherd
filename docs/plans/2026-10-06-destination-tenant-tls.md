# Destination tenant header and TLS options (#261, F-DEST-TLS)

Status: **design approved 2026-10-06** (decisions in §9); building in the §10 order, one PR per step. Follows #229 (`docs/plans/2026-10-01-destination-auth.md`, shipped in #260) and #262
(#263/#264/#265). Line references are to `main` at 3e10234.

## Problem

`docs/spec.md` §11.2 (line 614) says it plainly: "Tenant headers and TLS options are not rendered."
`wizard.RenderWriter` (`internal/wizard/destination.go:136-207`) emits `name`, `url` and the auth
block, and nothing else. Two consequences:

- **The tenant is stored and ignored**, the same bug class #229 fixed for auth. `destinations.tenant_id`
  exists (`0001_init.up.sql:65`, `NOT NULL DEFAULT ''`, no CHECK), is on the proto
  (`destination.proto:47`) and is written unvalidated by `CreateDestination`/`UpdateDestination`
  (`rpc_destination.go:255`, `:327`), but `wizardDestinations` never reads it (`:223-226`). The SPA
  never sets it (`DestinationsPage.tsx:147` sends `''`, `:177` preserves the stored value), so only API
  callers can have set one.
- **No TLS options**, so a backend behind a private CA, or one that requires a client certificate,
  needs a hand-written pipeline.

## 1. Goals / non-goals

Goals:
- A wizard writer sends a destination's tenant (`X-Scope-OrgID`) to Mimir and Loki.
- A wizard writer can trust a private CA, present a client certificate, and set `server_name`, with
  all material read on the collector from a Kubernetes Secret or ConfigMap. Shepherd stores only
  references and fixed key names, never PEM (the #229 model, spec §11.4).
- Same rendering point (`RenderWriter`), same gate, same re-render path as #229/#262.

Non-goals:
- **OTLP writers.** No wizard emits `otelcol.exporter.*` (the only `RenderWriter` callers are the six
  wizards, all `WriterPrometheus`/`WriterLoki`). The contract below maps 1:1 onto `otelcol` `client.tls`
  (same `ca_pem`/`cert_pem`/`key_pem`/`server_name` attributes in the pinned schema) and `client.headers`,
  so an OTLP writer can adopt it later without a new contract.
- The receiver tier. It renders from an operator file (`shepherd receiver render`,
  `internal/cli/receiver.go:37`), not from destination rows, and keeps D10 pass-through untouched.
- `min_version`, cipher suites, TLS for the OAuth2 token endpoint (`oauth2.tls_config`), per-entry
  Loki tenancy (`stage.tenant`), and `insecure_skip_verify` (Q3: not offered).
- Checking that the Secret/ConfigMap exists or that the certificates are valid: Shepherd never reads
  them (§5).

## 2. Tenant

### Where the value comes from

Three candidate sources:

| Source | Set by | Applies to |
|---|---|---|
| `destinations.tenant_id` | org admin (destination writes are org-admin) | the writer for that destination |
| `orgs.tenant_id` (0013, #204) | app admin, set once, unique across orgs (D11) | today only tenant routes (gateway ingress) |
| D10 pass-through | the gateway's `HTTPRoute` header filter | receiver-tier `otelcol` exporters only |

**D10 does not apply to wizard writers.** Pass-through propagates an *inbound request's* header to the
exporters of the same receiver pipeline (`otelcol.auth.headers` with `from_context`,
`internal/receiver/render.go:148-151`). A wizard writer scrapes or tails locally; there is no inbound
request whose context could carry a tenant.

**Decision: render `destinations.tenant_id`** (the field already on the API), validated, with a
cross-org guard (Q2): refused if it equals **another** org's `orgs.tenant_id`. The form pre-fills it
from the org's own tenant.

### Does an egress tenant header weaken the gateway tier? No.

The gateway rule (D11, `gateway-tier-plan.md:240-260`; `RenderHTTPRoute` *sets*, never appends,
`X-Scope-OrgID`) protects **ingress**: unauthenticated external clients must not choose the tenant
their data lands in. A destination tenant is **egress** from the org's own collectors, and:

1. **It confers nothing new.** Any org editor can already write a raw pipeline with
   `headers = {"X-Scope-OrgID" = "anything"}`; no gate inspects headers. A rendered field is no wider.
   The backend's own auth (or its network policy) is the egress boundary, as it is today.
2. **It cannot reach a receiver pipeline's exporters.** Receiver pipelines are not rendered from
   destinations; `validatePassThroughExporter` refuses a literal tenant header on a pass-through
   pipeline (`internal/receiver/validate.go:220-235`); and every served pipeline is a separate
   `declare` module (`internal/merge/merge.go:239`), so a wizard writer cannot sit on, or rewrite, a
   receiver pipeline's export path.
3. **It cannot reach the ingress header.** The gateway sets `X-Scope-OrgID` on the hop *into* the
   receiver; a wizard writer never sends to the receiver tier unless its URL points there, and then
   the gateway overwrites whatever it sent.

What *is* worth closing is D11's **one tenant, one org** property on Shepherd-managed paths: an org
admin naming another org's `orgs.tenant_id` on a shared backend. Q2 closes it.

### Validation

Empty means "no tenant header". Non-empty must pass `gateway.ValidateTenantID`
(`internal/gateway/tenant.go:48`, Mimir's charset, ≤150 bytes, not `.`/`..`/`__mimir_cluster`) — the
rule bindings already use (`rpc_destination.go:636`). Enforced in `CreateDestination`/`UpdateDestination`
(`invalid_argument`) and again in `RenderWriter` (a row written before this change may not comply; it
is then refused at render with a message naming the destination, not shipped). No CHECK migration:
existing rows may violate it, and Go-side refusal at both points is enough.

### Rendering

| Writer | Alloy |
|---|---|
| `prometheus.remote_write` | `endpoint { … headers = { "X-Scope-OrgID" = "<tenant>" } … }` — `prometheus.remote_write` has no `tenant_id` attribute |
| `loki.write` | `endpoint { … tenant_id = "<tenant>" … }` — the documented attribute; the Loki client sets `X-Scope-OrgID` from it and batches per tenant |

Values go through `wizard.Quote`. The key is `gateway.TenantHeader` (`internal/gateway/route.go:50`),
not a second literal. `loki.write`'s `tenant_id` is overridden per entry by a `__tenant_id__` label;
no wizard emits `stage.tenant` or a `labelmap`, and Kubernetes label names cannot begin with `__`, so
nothing a workload controls reaches it. A wizard that adds `stage.tenant` later must revisit this.

## 3. TLS

### Key contract

Material lives in objects on each spoke cluster, read with `remote.kubernetes.secret` /
`remote.kubernetes.configmap`. Key names follow `kubernetes.io/tls` (and what cert-manager writes).
`tls.crt`/`tls.key` are fixed; the CA key defaults to `ca.crt` and can be overridden per destination
(Q5), because trust-manager `Bundle`s write a configurable key:

| Part | Object | Keys | Alloy attribute |
|---|---|---|---|
| Trusted CA | Secret **or** ConfigMap | `ca.key`, default `ca.crt` | `tls_config.ca_pem` |
| Client certificate | Secret (`kubernetes.io/tls` or Opaque) | `tls.crt`, `tls.key` | `cert_pem`, `key_pem` |
| Server name | — (destination metadata) | — | `server_name` |

- CA and client certificate are **independent** (private CA without mTLS; mTLS against a public CA).
  Because Shepherd never sees the objects it cannot render an optional key conditionally (the #229
  scopes argument), so the destination declares which parts exist.
- A cert-manager `Certificate` Secret carries all three keys; pointing CA and client at the same
  Secret renders **one** `remote.kubernetes.secret`.
- `kube-root-ca.crt` (every namespace, key `ca.crt`) works as a CA ConfigMap when the backend's
  certificate is issued by the cluster CA.
- A `ca.crt` with several PEM blocks (a bundle) is accepted by Alloy as-is.

### Destination fields (stored in `extra.tls`, see §4)

```json
{"tls": {
  "ca":          {"kind": "configmap", "namespace": "monitoring", "name": "backend-ca", "key": "trust-bundle.pem"},
  "client_cert": {"namespace": "monitoring", "name": "collector-mtls"},
  "server_name": "mimir.internal.example"
}}
```

All three are optional; `{}` or an absent `tls` means "system trust store, no client cert", which is
today's behaviour. `ca.kind` is `secret` or `configmap`; `ca.key` is optional (absent or empty means
`ca.crt`) and must be a legal ConfigMap/Secret data key (`[-._a-zA-Z0-9]+`, at most 253 characters,
not `.` or `..` — Kubernetes' `IsConfigMapKey`).

### `insecure_skip_verify`: not offered (Q3, decided)

It would let a Secret-backed password or OAuth2 client secret (#229) be sent to anyone on the path,
undoing what #229 bought. The common reason to want it — a private CA — is exactly what this change
supports. Raw pipelines remain the escape hatch.

### Rendering (both writers; `<l>` is the writer label, `metrics` or `logs`)

```alloy
remote.kubernetes.configmap "<l>_tls_ca" {           // ca.kind = configmap
  namespace = "monitoring"
  name      = "backend-ca"
}
remote.kubernetes.secret "<l>_tls_client" {           // client_cert set (also serves ca when the same Secret)
  namespace = "monitoring"
  name      = "collector-mtls"
}

prometheus.remote_write "<l>" {                        // or loki.write
  endpoint {
    name = "…"
    url  = "https://…"
    headers = { "X-Scope-OrgID" = "acme" }            // §2 (loki.write: tenant_id = "acme")
    basic_auth { … }                                   // #229, unchanged
    tls_config {
      ca_pem      = remote.kubernetes.configmap.<l>_tls_ca.data["trust-bundle.pem"]  // ca.key, default "ca.crt"
      cert_pem    = convert.nonsensitive(remote.kubernetes.secret.<l>_tls_client.data["tls.crt"])
      key_pem     = remote.kubernetes.secret.<l>_tls_client.data["tls.key"]
      server_name = "mimir.internal.example"
    }
  }
}
```

- Types (pinned `alloy-v1.20.1` schema): `ca_pem`, `cert_pem`, `server_name` are `string`;
  `key_pem` is `secret`. A Secret's `data` is `map(secret)`, so the CA key from a Secret and `tls.crt`
  go through `convert.nonsensitive` (as #229 does for `username`); a ConfigMap's `data` is
  `map(string)` and needs none. `tls.key` is never converted.
- Component labels: `<l>_auth` (#229), `<l>_tls_ca`, `<l>_tls_client`. When two references name the
  same object (same kind, namespace and name — e.g. auth and client cert in one Secret, or CA and client
  cert in one cert-manager Secret) the first label is reused and the object is read once.
- Fixed order: auth Secret, CA, client Secret, then the writer; attributes in the order above. This
  keeps the output a pure function of the destination, which the fingerprint and the hand-edit
  fallback depend on (§6).
- `tls_config` is omitted entirely when `extra.tls` has no parts.

## 4. API / proto

**Tenant:** no change. `tenant_id` is already field 6 on `Destination` and field 5/6 on
Create/UpdateDestinationRequest.

**TLS: `extra.tls`, no proto change (Q4, decided)**, as #229 did for `extra.oauth2_scopes` (`extra` is a
`Struct`, `destination.proto:53`). `wizard` gains `ExtraKeyTLS = "tls"` and a `TLS` struct on
`wizard.Destination`; `rpc_destination.go` decodes it next to `destinationScopes` (`:168`). The
binding requests already refuse any `extra` (`rpc_destination.go:480`), so a binding cannot set it.

**Robustness fix that ships first.** `wizardDestinations` returns an error for the
whole org if *any* destination's `extra` fails to decode (`rpc_destination.go:219-222`), so one bad
row breaks every wizard render and every destination update in the org. `extra` has always been
free-form ("arbitrary shape", `destination.proto:52`), so a caller may already hold an `extra.tls` of
another shape. Change it to record a per-destination decode error and refuse only renders that
**name** that destination.

## 5. Validation

| Where | What |
|---|---|
| `CreateDestination` / `UpdateDestination` (`invalid_argument`) | `tenant_id` empty or `ValidateTenantID`, plus Q2's guard; `extra.tls` decoded strictly (unknown keys refused, so `insecure_skip_verify` or a typo is an error, not silently dropped); every ref's namespace/name matches `k8sNamespaceRE`/`k8sNameRE` (`destination.go:105-108`); `ca.kind` ∈ {secret, configmap}; `ca.key` a legal data key when set; `server_name` an RFC 1123 hostname; any TLS part requires an `https://` URL (`validateURL` accepts `http`, `:211`) |
| `RenderWriter` | the same checks again (rows written before this change, or by a path that skipped the API) — refuse, naming the destination |
| Gate Stage 1–2 | the rendered text parses; component, block and attribute names exist in the pinned schema; `alloy validate` on the result. Goldens run through the real pinned Alloy, as #229's do |
| Gate Stage 3 | merged config valid with the re-rendered pipelines swapped in (unchanged, #262) |
| Only the collector / e2e | the objects exist; the keys exist; the RBAC allows reading them; the PEM parses; the chain verifies; `server_name` matches the SAN; the backend accepts the client cert and the tenant |

Whether Stage 2 catches a `secret` passed where a `string` is expected (a missing
`convert.nonsensitive`) is to be established red-first while building; the e2e proves it regardless.

## 6. Interaction with #262 (#263/#264/#265)

- **Re-render on destination change:** nothing new. `UpdateDestination` already re-renders from
  `wizardDestinations` before/after (`rpc_destination.go:317`, `:342`); adding the fields to
  `wizard.Destination` and to `wizardDestinations` is all it needs. One consequence to state in the
  spec: a tenant-only edit now **does** change the writer and writes a revision — the comment at
  `destination_rerender.go:76` ("a tenant_id, say") becomes wrong and must change.
- **Fingerprint:** nothing new. A pipeline with `wizard_render_sha256` is judged by its hash alone
  (`destination_rerender.go:184-189`), so a renderer that now emits a tenant header does not make it
  look hand-edited.
- **Hand-edit fallback needs one change.** A pipeline *without* a fingerprint (pre-0030, or cleared
  by an editor write) is compared byte-for-byte with a fresh render against `before`
  (`:191-205`). For a destination with a non-empty `tenant_id`, that render now contains a header the
  stored text (rendered by the pre-#261 renderer) lacks, so an untouched pipeline is refused as
  "edited by hand". Fix: the pre-#261 renderer's output is exactly the current renderer with `Tenant`
  and `TLS` zeroed, so the fallback accepts a match against either render. Red-first test: an
  unfingerprinted pipeline from the old renderer + a destination with a tenant is regenerated, not
  refused.
- **Delete guard, rename, Detach from wizard:** unchanged.
- **Existing tenant values start being sent** when each pipeline is next regenerated (a wizard
  commit, a destination update, or the CLI). That is a served-config content change for existing
  data. Approved for this feature (Q1); operators trigger it with
  `shepherd admin rerender-destinations --all`.

## 7. Collector RBAC; docs and onboarding

- **RBAC:** the collector's ServiceAccount needs `get`/`list`/`watch` on `secrets` in the client-cert
  (and CA, if a Secret) namespace, and on `configmaps` in the CA namespace if a ConfigMap. The
  `grafana/alloy` chart's default ClusterRole grants both for `remote.kubernetes.*`; verify against the
  chart version pinned at build time and say so in the docs. Restricted collectors need a Role per
  namespace; the docs show one. `internal/chartvalues` stays `remoteConfig`-only (as #229).
- **Docs:** `scripts/docs-content/destinations.html` gains "Tenant" and "TLS from a Secret or
  ConfigMap" sections (key table, cert-manager and `kube-root-ca.crt` examples, the RBAC rule, why
  there is no skip-verify); `make docs`. Spec §11.2 line 614 and §11.4 updated with the contract.
  `UPGRADING.md` gets a section for the `--all` operator step (Q1).
- **Onboarding (`internal/onboarding`):** no change. It renders *ingress* artifacts for apps sending
  to a tenant route; the gateway injects the tenant there.
- **SPA:** the destination form gets a Tenant input (pre-filled with the org's tenant ID when the org
  has one) and a collapsible TLS section (CA source + reference, client-cert Secret, server name);
  create sends `tenantId` (no longer `''`); the table's Tenant column (spec §13) shows it.

## 8. Test plan

- `internal/wizard` (red first): goldens per writer for tenant only; CA from ConfigMap (default key and an overridden `ca.key`); CA from
  Secret; mTLS with CA and client cert in one Secret (one component); mTLS + `basic_secret` + tenant
  + `server_name`; all through the real pinned Alloy (Stage 1-2 incl. port shapes). Refusals:
  invalid tenant, TLS on `http://`, bad refs, unknown `extra.tls` keys, `insecure_skip_verify`. Every
  existing golden byte-identical (no tenant, no TLS ⇒ no change).
- `internal/mgmtapi`: Create/Update `invalid_argument` cases; the cross-org tenant guard; a tenant edit re-renders and
  writes a revision; the hand-edit fallback accepts a pre-#261 render (§6); one destination with a bad
  `extra` no longer breaks renders that do not name it.
- Web: unit tests for the form's extra/tenant mapping; a mocked Playwright spec for the TLS section.
- **`e2e/k8s` `TestDestinationTenantTLS`** (extends `destination_auth_test.go`'s shape):
  - The test generates a CA, a server certificate (SAN = the sink Service DNS name) and a client
    certificate in Go (`crypto/x509`; no cert-manager). It creates the CA ConfigMap and a
    `kubernetes.io/tls` client Secret; collector RBAC is a Role on exactly `secrets` + `configmaps` in
    that namespace.
  - Sink: HTTPS with that server cert, **requiring and verifying** a client cert from the CA, logging
    each request's path, `X-Scope-OrgID` and client-cert CN. Use `mendhak/http-https-echo` (already
    used by the receiver tests) only if its mTLS mode *rejects* a missing cert — to check at build time;
    otherwise a ~40-line stdlib Go sink run from a ConfigMap on the pinned `GO_IMAGE`.
  - Destinations: `prometheus` and `loki`, tenant `acme`, CA + client cert; a wizard pipeline with
    metrics and logs (`appobservability`).
  - Assert: remote-write and Loki pushes arrive with `X-Scope-OrgID: acme` and the expected client CN,
    and none without; the served config contains no `BEGIN` PEM marker.
  - Negative: a second destination identical except its CA ConfigMap holds a *different* CA ⇒ zero
    requests from it at the sink and an x509 unknown-authority error in the collector log (proves the
    CA reference is what decides trust, not the system pool). Red run: drop `tls_config` from the
    renderer ⇒ the positive assertions fail.

## 9. Decisions (maintainer, 2026-10-06)

The design PR (#271) put five questions; the answers:

| # | Question | Decision |
|---|---|---|
| Q1 | Rollout of tenants already stored | **Operator-triggered.** A stored tenant is rendered on the pipeline's next regeneration (wizard commit, destination update), plus `shepherd admin rerender-destinations --all` (dry run by default; lists destinations with a non-empty tenant and the pipelines that would change) and an `UPGRADING.md` note. The served-config content change is **approved** for this feature. |
| Q2 | Who may choose the egress tenant | Any valid tenant ID, **refused with a generic message if it equals another org's `orgs.tenant_id`**. The form pre-fills the org's own tenant. |
| Q3 | `insecure_skip_verify` | **Not offered**: refused as an unknown `extra.tls` key. |
| Q4 | `extra.tls` or typed proto fields | **`extra.tls`**, no proto change. |
| Q5 | CA key name | **Optional per-destination override** `extra.tls.ca.key`, default `ca.crt`, validated as a legal ConfigMap/Secret key; tested with the default and an override. |

## 10. Effort and sequence

| Step | Content | Size | Depends on |
|---|---|---|---|
| 1 | Per-destination `extra` decode errors (§4) | S | — (independent bug fix) |
| 2 | Tenant: validation, cross-org guard, rendering, hand-edit fallback, spec/docs, form field | S | — |
| 3 | TLS: contract (incl. `ca.key`), validation, rendering, goldens, spec/docs, form section | M | step 1 |
| 4 | `TestDestinationTenantTLS` (e2e-k8s) | M | 2, 3 |
| 5 | `rerender-destinations --all` + `UPGRADING.md` | S | 2 |

Steps 1, 2 and 3 can each ship alone; 2 and 3 carry their own unit and real-Alloy proofs, and step 4
is the deployed-artifact proof AGENTS.md requires before the CHANGELOG lists either as Shipped.
