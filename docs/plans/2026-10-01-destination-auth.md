# Destination Secret auth (#229, F-DEST-AUTH)

Status: building on `feat/destination-secret-auth`. Maintainer decision 2026-10-01: build it, and the
served-config content change (auth rendered into served config) is approved for this change only.
No proto change.

## Problem

`destinations.auth_mode` `basic_secret` / `oauth2_secret` plus `secret_namespace`/`secret_name` are
stored and returned, but nothing renders them. Every wizard writer emitted
`url = sys.env("SHEPHERD_DEST_<NAME>_URL")` and a `// auth injected by Shepherd at serve time`
comment, and nothing injected anything. A destination in a Secret mode shipped with no auth. Nothing
set that environment variable either, so the URL the operator typed into the destination was never
used.

## Decisions

### 1. One URL model: the destination's `url`, rendered verbatim

The destination row's `url` becomes the writer's endpoint URL, as a string literal: the full push
URL (`…/api/v1/push` for Prometheus remote-write, `…/loki/api/v1/push` for Loki). Shepherd adds no
path. Two alternatives were rejected:

- **`sys.env("SHEPHERD_DEST_<NAME>_URL")`**: no Shepherd artifact sets that variable, and the name
  mangling (`prom-prod` and `prom_prod` both become `PROM_PROD`) is lossy.
- **A `url` key in the Secret** (the shape sketched in spec §11.2): a URL is not a credential. The
  destination form requires one, it is shown in the table, and keeping a second copy in the Secret
  gives the two copies room to disagree.

### 2. The Secret key contract

The Secret lives on each spoke cluster at `secret_namespace`/`secret_name`. Shepherd stores and
serves only those two names and the key names below. It never reads, stores or serves the values.

| auth_mode | Required keys | Rendered into the writer's `endpoint` |
|---|---|---|
| `none` | none | nothing |
| `basic_secret` | `username`, `password` | `basic_auth { username, password }` |
| `oauth2_secret` | `client_id`, `client_secret`, `token_url` | `oauth2 { client_id, client_secret, token_url [, scopes] }` |

`password` and `client_secret` are passed as Alloy `secret` values. `username`, `client_id` and
`token_url` are typed `string` in Alloy, so they go through `convert.nonsensitive(...)`.

OAuth2 **scopes are not a Secret key**. Shepherd never sees the Secret, so it cannot know whether an
optional key is present, and Alloy has no expression that turns an absent key into "omit this
attribute". Scopes are not sensitive either. They are destination metadata: `extra.oauth2_scopes`, a
list of strings that the destination form sets. No proto change is needed because `extra` is
already a `Struct`.

### 3. Where rendering happens: wizard commit, in one shared renderer

Only wizards turn a destination row into config. The visual builder wires its own
`remote.kubernetes.secret` nodes. The receiver tier renders from an operator-supplied file
(`shepherd receiver render`). Raw and git pipelines write their own writers. So the single right
layer is:

- `internal/wizard`: `Destination`, `Destinations` and `RenderWriter(...)`. Each writer
  (`prometheus.remote_write`, `loki.write`) and its `remote.kubernetes.secret` are emitted by this
  one function, and all six wizards call it. `Wizard.Commit` takes the resolved
  `Destinations` alongside the state.
- `internal/mgmtapi` `WizardService`: `RenderWizard` and `CommitWizard` both load the org's
  destinations and pass them in, so the preview and the stored pipeline are always the same text.

Rendering at serve time was rejected. Stage 2 (`alloy validate` plus the port-shape check) and
Stage 3 would then validate text that differs from what collectors receive. With commit-time
rendering the auth block is part of the stored pipeline: it shows in the editor and in revision
history, and it passes the full gate like any other line.

Consequences:

- A wizard naming a destination the org does not have, or one of the wrong type, now fails with
  `failed_precondition` and says which. Before, it rendered an env-var URL nothing set.
- A destination edit reaches a pipeline when its wizard is next committed. Pipelines already
  stored keep the auth they were rendered with. Nothing re-renders them automatically (left open, see
  the PR).
- Pipelines committed before this change keep their `sys.env(...)` URL and no auth until they are
  re-created.

### 4. RBAC on the spoke

`remote.kubernetes.secret` reads the Secret with the collector's own ServiceAccount: `get`, `list`
and `watch` on `secrets` in `secret_namespace`. The default ClusterRole of the `grafana/alloy` chart
(and so of k8s-monitoring's collectors) already grants this cluster-wide. A collector with
restricted RBAC needs a Role and RoleBinding in the Secret's namespace. The docs page shows one.
Shepherd's chart-values generator emits only `remoteConfig` by design (`internal/chartvalues`
doc), so it does not grow RBAC.

### 5. API validation

`CreateDestination`/`UpdateDestination` refuse (`invalid_argument`) an unknown `auth_mode`, and a
Secret mode without a valid Kubernetes namespace and name, instead of failing later at render time.

## Tests

- `internal/wizard`: goldens for each writer kind and mode (2 × 3), plus scopes and refusals, run
  red first. The same goldens go through the real pinned Alloy (`wizardtest.AlloyBinary()`), Stage 1
  and Stage 2 including port shapes.
- Every wizard: its existing goldens re-rendered against a shared fixture of destinations, plus one
  Secret-mode golden. All of them go through real Alloy.
- `internal/mgmtapi`: commit with a `basic_secret` destination stores the auth block. An unknown
  destination and a wrong type are `failed_precondition`. Destination validation is
  `invalid_argument`.
- Web: unit tests and a mocked Playwright spec for the Secret-key explanation. "Not applied yet" is
  gone.
- `e2e/k8s` (`TestDestinationSecretAuth`): a Secret, a `basic_secret` destination and a wizard
  pipeline. A real Alloy collector (restricted RBAC: a Role on the Secret's namespace only) loads
  the served config. A header-recording sink sees remote-write requests with the Secret's
  `Authorization: Basic …`, and the served config does not contain the password.
