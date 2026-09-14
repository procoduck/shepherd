# Reusable kind dev stack — implementation plan (2026-09-14)

Builds `make dev-kind`: a single-node kind cluster named `shepherd-dev` running the real chart
(CloudNativePG, Gateway API + NGINX Gateway Fabric, Calico), the existing dev seed, three Alloy
agents from `dev/*.alloy`, Gitea, and the navikt mock OIDC provider — everything reachable on
host port 80 under `*.localtest.me`. It is the Kubernetes flavour of `make dev`, not a second e2e
suite. Every root cause and constraint below was read in the code before the design was fixed;
file:line references are to `feat/kind-dev-stack` at `b43d284` (= `main` after the walkthrough
fixes, #67).

Integration branch: `feat/kind-dev-stack`. Seven slices, built in parallel on branches
`feat/kind-<key>` cut from it, each owning a **disjoint** file set so the merges are mechanical.
The orchestrator merges, adds the two cross-cutting lines in §4, and performs the live bring-up
(§5). **No slice creates, uses or deletes a kind cluster, and no slice runs `make e2e-k8s` or
`make dev-kind`** — several agents share this laptop; the orchestrator runs the cluster afterwards.
Every slice verifies statically (repocheck specs, `helm template`/`helm lint`, `kubectl create
--dry-run=client`, `bash -n`, `shellcheck`, `go vet`) and lists under "Live checks" what only a
real cluster can prove.

Repository rules (AGENTS.md, CONTRIBUTING.md) apply verbatim and are not restated, except the
ones that shape every slice: a red run first for every new control (write the failing check,
run it, capture the failure verbatim in the report and the commit body, then make it pass);
never weaken an existing test or guard (`.trivyignore` included); never touch
`internal/spa/dist`; no ask-first change (dependency, `proto/`, RBAC semantics, served-config
content/hash) — none is needed here; anything that would need one goes to §6.

## 0. Decisions and corrections as constraints, each re-verified

The four decisions signed 2026-09-14 and the review's corrections, with what the code actually
says. Where the code disagrees with the review, the code wins and the row says so.

| # | Constraint | Verified in code | Consequence for this plan |
|---|---|---|---|
| D1 | **CNI: install the suite's pinned Calico** so the chart's default-deny simulator policy is real in dev. | `e2e/k8s/main_test.go:58` pins `projectcalico/calico/v3.28.2`; `:184-192` applies it after `disableDefaultCNI` (`testdata/kind-cluster.yaml:14`); `deploy/helm/shepherd/values.yaml:620-623` enables the simulator by default with its NetworkPolicy. | The dev cluster config also sets `disableDefaultCNI: true` and `podSubnet: 10.244.0.0/16` (`kind-cluster.yaml:24`, and the OrbStack reason at `:15-23`); the script applies Calico from `CALICO_VERSION` and waits for nodes Ready + CoreDNS Available exactly as `main_test.go:197-225` does. |
| D2 | **Pins move to `deploy/versions.env`**: `KIND_NODE_IMAGE` and `CALICO_VERSION`, tag-pinned with a comment, Renovate-excluded for the node image; the suite reads them, `E2E_K8S_NODE_IMAGE` still wins. Gitea and mock-oauth2 pins stay in the kind manifests. | `main_test.go:132-137` — env wins, else literal `kindest/node:v1.31.4`; `route_conformance_test.go:393-407` `readVersionsEnvValue` already reads `../../deploy/versions.env`; `renovate.json:44-45` regex matches any `KEY=repo:tag` line in versions.env, so `KIND_NODE_IMAGE=kindest/node:v1.31.4` **would** get digest-pin and bump PRs unless excluded; `:19-24` is the precedent for a per-package `enabled: false`. Floors that bound the tag: NGF 2.6.0's chart `kubeVersion: '>= 1.31.0-0'` (read from the OCI chart, and `route_conformance_test.go:281-286`), CNPG 0.29.0's `'>=1.29.0-0'`. | Slice K2. **Extension, flagged for veto**: `NGF_CHART_VERSION=2.6.0` also moves there, because the script needs the same NGF pin the suite installs and today it is a Go constant (`route_conformance_test.go:298`) the script could only copy or grep. `CALICO_VERSION` and `NGF_CHART_VERSION` have no `image:tag` shape, so Renovate's regex never matches them. Every versions.env change bills `e2e-k8s`, `e2e-sim` and `schema-verify` on the PR (`e2e-k8s.yml:38`, AGENTS.md CI cadence) — accepted once. |
| D3 | **Router: Gateway API + NGINX Gateway Fabric**, everything on port 80 under `*.localtest.me`, HTTPRoutes for shepherd/oidc/gitea, ONE CoreDNS rewrite of the zone to the NGF Service. | Suite installs the CRDs from `GATEWAY_API_VERSION`/`_CHANNEL` (`versions.env:45-46`, `route_conformance_test.go:323-345`) and NGF `2.6.0` as release `ngf` in `nginx-gateway-system`, GatewayClass `nginx` (`:298-306`, `:376-386`); NGF provisions one data-plane Service per Gateway, found by label `gateway.networking.k8s.io/gateway-name=<gw>` (`:463-477`). NGF 2.6.0's chart values expose `nginx.service.type` (enum incl. `NodePort`) and `nginx.service.nodePorts: [{port, listenerPort}]` ("Each NodePort MUST map to a Gateway listener port") — read from `helm show values oci://ghcr.io/nginx/charts/nginx-gateway-fabric --version 2.6.0`; the `NginxProxy` CRD carries the same `kubernetes.service.nodePorts`. The chart's HTTPRoute takes `route.hostnames`/`parentRefs` verbatim and targets `<fullname>:service.port` (`templates/httproute.yaml:9-24`). | NGF is installed with `nginx.service.type=NodePort` and `nodePorts[0]={port:30080, listenerPort:80}`; kind maps node port 30080 → host port 80. One Gateway `dev` (listener `http`/80) in the release namespace; the chart's own route plus two hand-written HTTPRoutes; CoreDNS rewrites `*.localtest.me` → the Gateway's data-plane Service. **Live-only**: that the provisioned Service really carries NodePort 30080 (§5 step 6). |
| D4 | **OIDC: values-declared issuer only.** The admin-UI path is guarded and cannot reach a private issuer. | `internal/auth/discovery.go:71-84` `dialGuard`, `:89-105` blocks loopback/private/link-local + CGNAT (`:112-120`); `:161-165` two clients; `:182-187` only `SourceHelm` gets the unguarded one; `config.go:59-66` a non-empty `oidc.issuer` makes the chart own the settings; `settings_admin.go:50-52` `HelmManaged`, `:70-76` the read-only banner text, `:93` **Test connection always probes with `SourceDatabase`**; `AdminAuthPage.tsx:55,186,251` — form, Save and Test are disabled when `!editable`. | `dev/kind/values.yaml` declares `config.oidc.*`; the client secret rides in the Secret as `SHEPHERD_OIDC_CLIENT_SECRET` (`config.go:357`). The SSO page is read-only against this stack **by design** (§5 step 10 states the expected banner and the exact refusal an admin would see with the chart issuer removed). |
| C1 | Seed's only compose hostname is `gitea:3000`; a Service named `gitea` in the release namespace resolves it, no env override. | `internal/cli/dev.go:518` `seedGiteaBaseURL = "http://gitea:3000"`, `:604,611` plain `http.DefaultClient`; `dev/*.alloy:3` `url = "http://shepherd:8080"`; `_helpers.tpl:8-19` fullname == release name when the release name contains the chart name → release `shepherd` gives Service `shepherd` (`templates/service.yaml:4,11`). | Release `shepherd`, namespace `shepherd-dev`, Services `shepherd`, `gitea`, `oidc`. Gitea admin credentials must equal `dev.go:519-520` and compose's `gitea-init` (`dev/docker-compose.dev.yaml:112-113`). |
| C2 | Mount `dev/*.alloy` as ConfigMaps and `dev/shepherd.dev.env` as the existing Secret — zero copies. | `values.yaml:380` `existingSecret`; `templates/deployment.yaml:85-88` `envFrom` it; `_helpers.tpl:236-244` the migrate Job reads the same Secret; `:306-322` with `cnpg.enabled` the chart injects `SHEPHERD_DATABASE_URL` from `<cluster>-app`/`uri`, overriding anything in the Secret. `dev/shepherd.dev.env` has no database URL (compose sets it per service, `:143,158,174`) and no OIDC values (`:8-12`). | `kubectl create secret generic shepherd-dev-env --from-env-file=dev/shepherd.dev.env --from-literal=SHEPHERD_OIDC_CLIENT_SECRET=…` (kubectl ignores `#` lines and blanks). The three ConfigMaps are `--from-file=config.alloy=dev/alloy-<x>.alloy`. Nothing in `dev/` is copied or edited. |
| C3 | `reload` needs a build annotation so Helm rolls the pods after the migrate hook; same tag ≠ new template; a bare `rollout restart` skips migrations. `pullPolicy=Never` + tag `local`. | `templates/deployment.yaml:24-41` template annotations = `checksum/config` + `podAnnotations`; `migrate-job.yaml:10-12` pre-install/pre-upgrade hook at weight -5 using the same image (`:57-59`); `values.schema.json` `podAnnotations` is a `labelMap` (string→string, top-level `additionalProperties: false`); `fixtures_test.go:327-335` `chartImageArgs` sets registry `""`, tag `local`, `pullPolicy=Never` for both images. | `reload` = `kind load` both images → `helm upgrade --install … --set-string podAnnotations.dev-build=<short sha>-<unix ts> --wait`. The values file pins the image tuple exactly as `chartImageArgs` does. |
| C4 | Single node; cluster `shepherd-dev` (`e2e-k8s-clean` sweeps `shepherd-e2e-*` only); explicit context on every call; `down` = `kind delete`. | `Makefile:695-698` sweeps `^shepherd-e2e-` only; `main_test.go:67` names e2e clusters `shepherd-e2e-*`; `kind-cluster.yaml:8-10,25-28` explains why e2e has 3 nodes (policy across a node boundary — irrelevant in dev); `chart_deps_test.go:118-126` already installs with `replicas=1`, `cnpg.instances=1`. | `dev/kind/cluster.yaml` has one control-plane node. Every `kubectl` goes through a wrapper adding `--context kind-shepherd-dev`, every `helm` through one adding `--kube-context kind-shepherd-dev`; a repocheck spec forbids bare calls. `down` is `kind delete cluster --name shepherd-dev`, documented as the `dev-reset` equivalent (local-path PVs die with the cluster). |
| C5 | Gitea needs a PVC (seed is check-then-skip on the credential row). | `dev.go:539-551` returns `gitea-demo(exists)` without touching Gitea; a Gitea restart on `emptyDir` would leave a credential pointing at a missing repo. | `dev/kind/gitea.yaml` has a 1Gi PVC at `/var/lib/gitea`; both lifecycles (CNPG PV, Gitea PV) end together at `kind delete`. |
| C6 | Keep the seed's static agent token; run the seed via `kubectl exec` in the app pod. | `dev.go:44-45,706-721` inserts the fixed token directly; `dev/*.alloy:5-6` embed it. `deploy/Dockerfile.local:43-45` — the image is distroless, entrypoint `/usr/local/bin/shepherd`, user nonroot; `deployment.yaml:94-95` + `_helpers.tpl:367-371` read-only root FS; the seed's git push is in-memory (`dev.go:19-25` memfs/memory storage). **Correction to the review's finding 9**: `SHEPHERD_DEV_ALLOW_STATIC_TOKEN` is read only by `token create` (`internal/cli/token.go:61`), never by `dev seed` — no env injection is needed (and the image has no `env` binary to inject with). `config.Load("")` (`dev.go:139`, `root.go:21`) reads the pod's env, where `SHEPHERD_DATABASE_URL` (CNPG) and the encryption key (Secret) already are. | `kubectl exec deploy/shepherd -- /usr/local/bin/shepherd dev seed`. Ordering is safe: `seedLocalUsers` calls `BootstrapAdmin` first (`dev.go:274-296`), so running after `serve` is a no-op there. |
| C7 | Keycloak is premature; mock-oauth2-server exercises the same path. | `internal/auth/providers.go:168-177` `generic` preset (subject `sub`, groups `groups`); `config.go:336-342` defaults are Entra-shaped (`subject_claim: oid`) and `:450-451` turns Microsoft Graph lookups on when provider is `entra`; `auth.go:463-470` falls back to `sub` when the configured subject claim is absent; `:484-486` app admin = any group in `auth.app_admin_group_ids`; `:508-520` groups come from the claim when Graph is off; `:601-646` the local form stays on the login page whenever a local user exists, so admin/admin keeps working beside SSO. | `config.oidc.provider: generic` (no Graph call, `sub` as subject), `display_name: "Mock SSO"`; app admin group `11111111-1111-1111-1111-111111111111` comes from `dev/shepherd.dev.env:15`; org groups are the seed's (`dev.go:41-43`). |
| C8 | Plain manifests, not a chart profile. | A profile would enter `make helm-lint` (`Makefile:700-710`), `deploy/helm/chart_test.go` and `values_reference.py` docs (`scripts/repocheck/values_test.go`). | Manifests are plain YAML; the only chart-side artefact is a values file. |
| C9 | **Where the manifests live — deviation from the memory note's `deploy/kind` wording, decided here with evidence.** | `make config-scan` (`Makefile:575-577`) and `security-scan.yml:180-184` run Trivy misconfiguration checks over **all of `deploy/`** at CRITICAL/HIGH with `exit-code 1`; `scripts/repocheck/security_scan_test.go:65-78` pins `scan-ref: deploy`. Probe run 2026-09-14 with the pinned `TRIVY_IMAGE` over a bare one-container Deployment: `Failures: 3 (HIGH: 3)` — `KSV-0014` (read-only root filesystem) and `KSV-0118` ×2 (default security context). Gitea writes to its data dir and mock-oauth2-server is a JVM; a read-only root FS for them is ceremony that proves nothing, and adding those IDs to `.trivyignore` would silence them for the chart too — a weakened control. | Manifests live in **`dev/kind/`** beside `dev/docker-compose.dev.yaml` and `dev/shepherd.dev.env`, the files they mount; the script is `scripts/dev-kind.sh`. `deploy/` keeps only production artefacts. Nothing under `deploy/` changes except `versions.env`. Each manifest still carries a sensible `securityContext` (non-root where the image allows) but that is hygiene, not a gate. |
| C10 | Build order and docs target. | AGENTS.md docs map: `docs/kind-test-environment-plan.md` is a live multi-session plan; `project-status.md` is the single ledger. | Docs land in `docs/kind-test-environment-plan.md` (new §11), the target tables in `docs/dev-guide.md` and `README.md`, `e2e/k8s/README.md` (pin location), AGENTS.md's image table and command line, one document-map line in `project-status.md`, one `CHANGELOG.md` Unreleased line. No new ledger. |

## 1. Contract every slice depends on

Names are fixed here so the slices can be built apart and meet at integration.

| Thing | Value |
|---|---|
| kind cluster / kube context / namespace / release | `shepherd-dev` / `kind-shepherd-dev` / `shepherd-dev` / `shepherd` |
| Host ↔ node mapping | `dev/kind/cluster.yaml` `extraPortMappings: containerPort 30080 → hostPort 80, listenAddress 127.0.0.1, TCP` |
| Hostnames | `shepherd.localtest.me`, `oidc.localtest.me`, `gitea.localtest.me` (public wildcard → 127.0.0.1; offline, add them to `/etc/hosts`) |
| NGF | release `ngf`, namespace `nginx-gateway-system`, GatewayClass `nginx`, chart version `NGF_CHART_VERSION`, values `nginx.service.type=NodePort`, `nginx.service.nodePorts[0].port=30080`, `nginx.service.nodePorts[0].listenerPort=80` |
| Gateway | `dev` in `shepherd-dev`, `gatewayClassName: nginx`, one listener `http` port 80 protocol HTTP, `allowedRoutes.namespaces.from: Same`; data-plane Service found by label `gateway.networking.k8s.io/gateway-name=dev` (expected name `dev-nginx`) |
| CoreDNS | in `kube-system/coredns` Corefile's `.:53 {` block, one line: `rewrite name regex (.*)\.localtest\.me <data-plane-svc>.shepherd-dev.svc.cluster.local answer auto` — inserted idempotently, then `rollout restart deploy/coredns` |
| Secret | `shepherd-dev-env` from `dev/shepherd.dev.env` + literal `SHEPHERD_OIDC_CLIENT_SECRET=dev-oidc-client-secret` |
| ConfigMaps | `alloy-metrics`, `alloy-logs`, `alloy-staging`, key `config.alloy`, from the matching `dev/alloy-*.alloy` |
| Alloy image | `dev/kind/alloy.yaml` carries the literal placeholder `__ALLOY_IMAGE__`; the script substitutes `ALLOY_IMAGE` from versions.env at apply time (`sed`), so the pin is never restated outside Renovate's files |
| Shepherd images | `shepherd:local`, `shepherd-simulator:local` (`kind load docker-image`), `pullPolicy: Never` |
| Chart values | `dev/kind/values.yaml`, see K3; build annotation key `dev-build` set by the script with `--set-string` |
| Helm timeouts | first install `--wait --timeout 10m` (CNPG bootstrap + migrate retries, `chart_deps_test.go:51`), reload `5m` |
| Gitea | Deployment `gitea`, Service `gitea` ports 3000 (http) and 2222 (ssh), PVC `gitea-data` 1Gi at `/var/lib/gitea`, env identical to compose `:68-78`; admin bootstrap by `kubectl exec deploy/gitea -- gitea admin user create --username shepherd-admin --password 'Sh3pherd-Admin-Pass-1' --email admin@shepherd.test --admin --must-change-password=false` tolerating "already exists" (compose `:110-114`) |
| Mock OIDC | Deployment/Service `oidc`, image `ghcr.io/navikt/mock-oauth2-server:6.0.1` (compose `:118`), env `SERVER_PORT=8090` + the compose `JSON_CONFIG` (`:122-127`, `interactiveLogin: true`); issuer `http://oidc.localtest.me/default` (the server derives it from the request Host, which is why the DNS rewrite makes pod and browser agree) |
| Seed | `kubectl exec deploy/shepherd -- /usr/local/bin/shepherd dev seed` |
| Script verbs | `scripts/dev-kind.sh up|reload|seed|status|down`; `up` is idempotent (re-runnable on an existing cluster) |
| Static tools in CI | the `guards` job (always-on, `ci.yml:256-304`) runs `go test ./scripts/repocheck/...` and has `helm` (it runs `make helm-lint`); repocheck specs may shell out to `helm template` but must **not** need a cluster — parse manifests with `yaml.v3` (already imported by `helpers_test.go:13`) instead of `kubectl apply --dry-run` |
| Local versions on this laptop (for the orchestrator, not the slices) | kind v0.33.0, helm v4.2.4, kubectl v1.36.4, docker 29.4.0, shellcheck 0.11.0 |

Repocheck helpers to reuse: `readRepoFile`, `loadYAML`, `runMake`, `makeRecipe`, `mkTargetLine`
(`scripts/repocheck/helpers_test.go:31-78`). Each slice adds its specs in a **new** file so the
sets stay disjoint; every spec comment records the red run the way `makefile_test.go:11-14` does.

## 2. Slices

Each slice: branch `feat/kind-<key>` from `feat/kind-dev-stack`; one commit (or a few) with the
trailers; red-run evidence (command + failing output, verbatim) in the report and the commit body.

### K1 — `script` · `scripts/dev-kind.sh` and the cluster config

**Files:** `scripts/dev-kind.sh` (new), `dev/kind/cluster.yaml` (new),
`scripts/repocheck/devkind_script_test.go` (new).

**Approach.** Bash, `set -euo pipefail`, header comment in the style of `scripts/build-web.sh:1-5`.
Constants from §1; `source deploy/versions.env` then fail loudly if `KIND_NODE_IMAGE`,
`CALICO_VERSION`, `NGF_CHART_VERSION`, `CNPG_CHART_VERSION`, `GATEWAY_API_VERSION`,
`GATEWAY_API_CHANNEL` or `ALLOY_IMAGE` is empty ("missing from deploy/versions.env — merge the
pins slice"). Two wrappers, `kc() { kubectl --context "$CTX" "$@"; }` and `hm() { helm
--kube-context "$CTX" "$@"; }`; nothing else calls `kubectl`/`helm` directly (`kind` takes
`--name "$CLUSTER"` everywhere). Verbs:

- `up`: create the cluster from `dev/kind/cluster.yaml` with `--image "$KIND_NODE_IMAGE"` unless
  `kind get clusters` lists it → apply Calico from
  `https://raw.githubusercontent.com/projectcalico/calico/${CALICO_VERSION}/manifests/calico.yaml`
  → `wait --for=condition=Ready nodes --all --timeout=5m` → `-n kube-system wait
  --for=condition=Available deploy/coredns` → `kind load docker-image shepherd:local
  shepherd-simulator:local` → Gateway API CRDs from
  `https://github.com/kubernetes-sigs/gateway-api/releases/download/${GATEWAY_API_VERSION}/${GATEWAY_API_CHANNEL}-install.yaml`
  → `hm upgrade --install ngf oci://ghcr.io/nginx/charts/nginx-gateway-fabric --version
  "$NGF_CHART_VERSION" -n nginx-gateway-system --create-namespace --set
  nginx.service.type=NodePort --set 'nginx.service.nodePorts[0].port=30080' --set
  'nginx.service.nodePorts[0].listenerPort=80' --wait --timeout 4m` → `hm upgrade --install cnpg
  oci://ghcr.io/cloudnative-pg/charts/cloudnative-pg --version "$CNPG_CHART_VERSION" -n
  cnpg-system --create-namespace --wait --timeout 5m` → namespace → `dev/kind/gateway.yaml` →
  wait for the Gateway's `Programmed` condition and for exactly one Service with the gateway-name
  label; assert its port-80 `nodePort` is 30080 (else fail with the observed value — this is the
  "verify at the consumed layer" check) → CoreDNS rewrite (idempotent insert + rollout restart)
  → Secret and the three ConfigMaps (`--dry-run=client -o yaml | kc apply -f -`) →
  `dev/kind/gitea.yaml`, `dev/kind/oidc.yaml`, `dev/kind/routes.yaml`, and `dev/kind/alloy.yaml`
  rendered through `sed "s|__ALLOY_IMAGE__|${ALLOY_IMAGE}|g"` → wait for `deploy/oidc` and
  `deploy/gitea` Available → Gitea admin bootstrap via exec (tolerating "already exists") →
  `hm upgrade --install shepherd deploy/helm/shepherd -n shepherd-dev -f dev/kind/values.yaml
  --set-string podAnnotations.dev-build="$(build_id)" --wait --timeout 10m` → `seed` → banner with
  the three URLs, `admin / admin`, and the `down` reminder.
- `reload`: `kind load` both images → the same `helm upgrade --install` with a fresh `dev-build`
  → `kc -n shepherd-dev rollout status deploy/shepherd`. Image builds are the Makefile's job (K6).
- `seed`: the exec from §1; `status`: `kc -n shepherd-dev get pods,svc,pvc,gateway,httproute`,
  `hm -n shepherd-dev status shepherd`, `kc -n kube-system get cm coredns -o jsonpath` grepped
  for the rewrite, and `curl -sf` of the three hostnames' health/discovery URLs from the host;
  `down`: `kind delete cluster --name "$CLUSTER"` (idempotent).
- `build_id()` = `git rev-parse --short HEAD`-`date +%s`.

`dev/kind/cluster.yaml`: `kind: Cluster`, `apiVersion: kind.x-k8s.io/v1alpha4`,
`networking.disableDefaultCNI: true`, `podSubnet: 10.244.0.0/16` (with the OrbStack comment
from `e2e/k8s/testdata/kind-cluster.yaml:15-23` condensed), one `control-plane` node with the
§1 port mapping, and a header saying why one node (dev gains nothing from three; the e2e file
keeps its three).

**Red run (write first).** `devkind_script_test.go`, `Describe("scripts/dev-kind.sh")`:
(a) the file exists, is executable, and `bash -n` passes; `shellcheck` runs clean when it is on
PATH (skipping shellcheck is allowed only for its absence, never for a finding);
(b) every line matching `^\s*(kubectl|helm)\s` is one of the two wrapper definitions —
i.e. after removing the `kc()`/`hm()` definition lines, `grep -E '^\s*(kubectl|helm) '` finds
nothing, and the definitions contain `--context "$CTX"` / `--kube-context "$CTX"`;
(c) no pin literal: none of `kindest/node:`, `calico/v3`, `nginx-gateway-fabric --version 2`,
`cloudnative-pg --version 0` appear; `KIND_NODE_IMAGE`, `CALICO_VERSION`, `NGF_CHART_VERSION`,
`CNPG_CHART_VERSION`, `GATEWAY_API_VERSION`, `ALLOY_IMAGE` do;
(d) `down` contains `kind delete cluster --name`; `reload` contains `podAnnotations.dev-build`;
`up` references every manifest name from §1 and `dev/kind/values.yaml`;
(e) `dev/kind/cluster.yaml` parses, has exactly one node, `disableDefaultCNI: true`,
`podSubnet: 10.244.0.0/16`, and a mapping `containerPort: 30080` / `hostPort: 80`.
Today every assertion fails on `open scripts/dev-kind.sh: no such file or directory` (record
the exact Gomega text from the first run).

**Acceptance.** Spec green; `bash -n scripts/dev-kind.sh` and `shellcheck scripts/dev-kind.sh`
clean; `kubectl create --dry-run=client --validate=false -f dev/kind/cluster.yaml` is **not**
applicable (kind config is not a Kubernetes object) — validate it with `kind create cluster
--config dev/kind/cluster.yaml --name x --dry-run`? kind has no dry-run: the YAML parse in the
spec is the static check; the orchestrator's `make dev-kind` is the live one. All existing
repocheck specs green.

**Check.**
```
bash -n scripts/dev-kind.sh && shellcheck scripts/dev-kind.sh
go test ./scripts/repocheck/ -count=1
```

**Live checks (orchestrator, §5).** cluster creates with the pinned image; Calico Ready; NGF
data-plane Service has nodePort 30080 on port 80; the CoreDNS rewrite resolves
`oidc.localtest.me` to the Service ClusterIP from a pod; `up` re-run on the live cluster is a
no-op-ish success; `down` leaves `kind get clusters` empty and `make e2e-k8s-clean` unaffected.

### K2 — `pins` · node image, Calico and NGF pins in `deploy/versions.env`

**Files:** `deploy/versions.env`, `e2e/k8s/main_test.go`, `e2e/k8s/route_conformance_test.go`,
`renovate.json`, `scripts/repocheck/kind_pins_test.go` (new).

**Approach.**
- `versions.env`: a new block after the operator pins (`:63-76`):
  ```
  # kind node image for BOTH the e2e suite (e2e/k8s/main_test.go, E2E_K8S_NODE_IMAGE still
  # wins) and the dev stack (scripts/dev-kind.sh). Tag only, no digest, and EXCLUDED from
  # Renovate (renovate.json): a bump must stay inside kind v0.33 / helm/kind-action support,
  # above NGF 2.6.0's kubeVersion floor (>= 1.31.0-0) and CNPG 0.29.0's (>= 1.29.0-0) —
  # a reviewed decision, not a Monday PR.
  KIND_NODE_IMAGE=kindest/node:v1.31.4
  # Calico manifest tag applied after cluster creation (disableDefaultCNI). A CNI upgrade
  # changing enforcement must be a deliberate commit — see e2e/k8s/main_test.go installCNI.
  CALICO_VERSION=v3.28.2
  # NGINX Gateway Fabric chart, the Gateway API controller the suite proves conformance
  # against and the dev stack routes through. 2.6.0 is the last line whose kubeVersion floor
  # is 1.31 (2.6.7 already needs 1.32) — see route_conformance_test.go for why NGF at all.
  NGF_CHART_VERSION=2.6.0
  ```
- `main_test.go`: delete the `calicoManifest` const (`:54-58`); `installCNI` reads
  `CALICO_VERSION` via `readVersionsEnvValue` and formats the URL; `kindNodeImage()` keeps the
  env override first and otherwise reads `KIND_NODE_IMAGE`, `log.Fatalf` on error (it runs in
  `TestMain` before any cluster exists). Comments move with the values.
- `route_conformance_test.go`: `ngfChartVersion` const → `installGatewayController` reads
  `NGF_CHART_VERSION`; keep the rationale comment (`:262-297`) above the remaining NGF constants
  and add one sentence pointing at versions.env. The comment at `:281-286` naming
  `kindest/node:v1.31.4` now says "the KIND_NODE_IMAGE pin".
- `renovate.json`: a `packageRules` entry `{"description": "The kind node image is
  tag-pinned by hand: … (same reasons as the versions.env comment)", "matchManagers":
  ["custom.regex"], "matchPackageNames": ["kindest/node"], "enabled": false}` — no
  `matchUpdateTypes`, so digest pinning is off too. Exactly one custom manager remains
  (`dependabot_test.go:110`).

**Red run (write first).** `kind_pins_test.go`, `Describe("kind pins")`:
(a) `deploy/versions.env` has `KIND_NODE_IMAGE=kindest/node:vX.Y.Z` (regex, **no** `@sha256`),
`CALICO_VERSION=vX.Y.Z`, `NGF_CHART_VERSION=X.Y.Z`, each with a `#` comment on the line above;
(b) `e2e/k8s/main_test.go` contains neither `kindest/node:` nor `projectcalico/calico/v`, and
contains `readVersionsEnvValue("KIND_NODE_IMAGE")`, `readVersionsEnvValue("CALICO_VERSION")`
and still `E2E_K8S_NODE_IMAGE`; `route_conformance_test.go` contains
`readVersionsEnvValue("NGF_CHART_VERSION")` and no `ngfChartVersion = "`;
(c) `renovate.json` has a rule with `matchPackageNames` containing `kindest/node` and
`enabled: false`.
Today: (a) fails with `Expected … to match regular expression (?m)^KIND_NODE_IMAGE=…`, (b) with
the `kindest/node:` literal at `main_test.go:136`, (c) with "no renovate rule disables
kindest/node". Record the text.

**Acceptance.** Spec green; `go vet -tags e2ek8s ./e2e/k8s/` clean; `make lint` 0 issues;
`make check-docker`, `make check-gateway-pin`, `make check-chartvalues-pin` unchanged and green
(`make guards`); `go test ./scripts/repocheck/ -count=1` green including the existing renovate
spec; `. deploy/versions.env && echo "$KIND_NODE_IMAGE"` prints the tag (the Makefile `include`s
and `export`s the file, `Makefile:12-13`, so an unquoted value must stay shell- and make-safe).

**Check.**
```
go vet -tags e2ek8s ./e2e/k8s/ && make lint && go test ./scripts/repocheck/ -count=1
. deploy/versions.env && test -n "$KIND_NODE_IMAGE" && test -n "$CALICO_VERSION" && test -n "$NGF_CHART_VERSION"
```

**Live checks.** The weekly/PR `e2e-k8s.yml` run on the integration PR stays green (it is the
only proof the suite still installs the same Calico/NGF/node image); the orchestrator does not
run `make e2e-k8s` locally for this — CI does, because the PR touches `deploy/versions.env`.

### K3 — `values` · `dev/kind/values.yaml`

**Files:** `dev/kind/values.yaml` (new), `scripts/repocheck/devkind_values_test.go` (new).

**Approach.** A commented values file (`values.schema.json` is strict at the top level, so only
known keys):
```yaml
replicas: 1                      # also suppresses the app PDB (effectiveMinReplicas)
image: { registry: "", repository: shepherd, tag: local, pullPolicy: Never }
simulator:
  image: { registry: "", repository: shepherd-simulator, tag: local, pullPolicy: Never }
existingSecret: shepherd-dev-env
cnpg: { enabled: true, instances: 1, storage: { size: 1Gi } }
route:
  enabled: true
  hostnames: [shepherd.localtest.me]
  parentRefs: [{ name: dev }]     # same namespace as the release
config:
  server: { base_url: http://shepherd.localtest.me }
  oidc:
    provider: generic             # sub as subject, groups claim, no Graph call
    display_name: Mock SSO
    issuer: http://oidc.localtest.me/default
    client_id: shepherd-dev       # secret: SHEPHERD_OIDC_CLIENT_SECRET in the Secret
    redirect_url: http://shepherd.localtest.me/auth/callback
```
`service.type` stays ClusterIP (the Gateway fronts it); `resources` stay `{}` (`values.yaml:520`);
simulator stays on (Calico makes its policy real, D1). No `podAnnotations` here — the script sets
`dev-build` per upgrade. Comments explain each line the way `deploy/helm/shepherd/ci/*.yaml` do.

**Red run (write first).** `devkind_values_test.go`: run `helm template shepherd
deploy/helm/shepherd -f dev/kind/values.yaml --namespace shepherd-dev`, split the documents,
and assert: the `Deployment/shepherd` has `replicas: 1`, container image `shepherd:local`,
`imagePullPolicy: Never`, `envFrom` secret `shepherd-dev-env`, and an env
`SHEPHERD_DATABASE_URL` from secret `shepherd-db-app` key `uri`; `Deployment/shepherd-simulator`
image `shepherd-simulator:local` / `Never`; `HTTPRoute/shepherd` hostnames
`[shepherd.localtest.me]`, parentRef `dev`, backend `shepherd` port `8080`; `Cluster/shepherd-db`
`instances: 1`, `storage.size: "1Gi"`; no `Secret/shepherd-secrets` rendered; the ConfigMap's
`shepherd.yaml` contains `issuer: http://oidc.localtest.me/default`, `provider: generic`,
`redirect_url: http://shepherd.localtest.me/auth/callback`, `base_url:
http://shepherd.localtest.me`. Also `helm lint --strict deploy/helm/shepherd -f
dev/kind/values.yaml` exits 0. Today: `helm template` fails with `open dev/kind/values.yaml: no
such file or directory` (record it).

**Acceptance.** Spec green; `make helm-lint` unchanged and green; the rendered NOTES print
"Access URL: https://shepherd.localtest.me" — known cosmetic misstatement (`NOTES.txt:9-10`
hardcodes https; see §6) — not fixed here.

**Check.**
```
helm lint --strict deploy/helm/shepherd -f dev/kind/values.yaml
helm template shepherd deploy/helm/shepherd -f dev/kind/values.yaml --namespace shepherd-dev > /dev/null
go test ./scripts/repocheck/ -count=1
```

**Live checks.** `helm upgrade --install` converges within 10m on a cold cluster (migrate Job
retries while CNPG bootstraps); Shepherd logs show OIDC discovery of the mock issuer succeeded
(or, if the provider was not yet routable at boot, that `refreshIfStale` picked it up on the
first `/auth/methods`); `/api/me` after SSO login reports the groups pasted (§5).

### K4 — `workloads` · Alloy agents and Gitea manifests

**Files:** `dev/kind/alloy.yaml` (new), `dev/kind/gitea.yaml` (new),
`scripts/repocheck/devkind_workloads_test.go` (new).

**Approach.**
- `alloy.yaml`: three Deployments `alloy-metrics`, `alloy-logs`, `alloy-staging` (1 replica),
  image `__ALLOY_IMAGE__`, args exactly compose's (`dev/docker-compose.dev.yaml:235-240`):
  `run /etc/alloy/config.alloy --storage.path=/tmp/alloy --server.http.listen-addr=0.0.0.0:12345
  --disable-reporting` (one port for all three — they are separate pods now), a `configMap`
  volume named after the ConfigMap from §1 mounted at `/etc/alloy` (read-only), an `emptyDir` at
  `/tmp`. Header comment: the placeholder is substituted by `scripts/dev-kind.sh` from
  `ALLOY_IMAGE` so the pin is never restated (AGENTS.md image table).
- `gitea.yaml`: PVC `gitea-data` (1Gi, RWO), Deployment `gitea` (`strategy: Recreate` — RWO PVC),
  image `gitea/gitea:1-rootless`, env copied verbatim from compose `:68-78`, ports 3000/2222,
  readiness `httpGet /api/healthz:3000`, PVC at `/var/lib/gitea`, `emptyDir` at `/tmp`; Service
  `gitea` ports 3000 and 2222. Header comment: why a PVC (C5), that the admin bootstrap is the
  script's exec, and that the Gitea pin lives here deliberately (D2).

**Red run (write first).** `devkind_workloads_test.go`, parsing both files with `yaml.v3` into
`[]map[string]any` (multi-document decode) and the compose file with `loadYAML`:
(a) `alloy.yaml`: exactly three Deployments; each mounts a ConfigMap named after itself at
`/etc/alloy`; each image is the literal `__ALLOY_IMAGE__`; the file contains no `grafana/alloy`
string; args contain `--disable-reporting` and `/etc/alloy/config.alloy`; the three names
match the three `dev/alloy-*.alloy` basenames (`os.ReadDir("dev")`);
(b) `gitea.yaml`: a PVC and a Deployment mounting it at `/var/lib/gitea`; a Service named
`gitea` with port 3000; the Deployment's `env` map equals compose's `services.gitea.environment`
map key-for-key (values compared as strings); the image equals compose's `services.gitea.image`.
Today: `open dev/kind/alloy.yaml: no such file or directory` (record).

**Acceptance.** Spec green; `kubectl create --dry-run=client --validate=false -f
dev/kind/alloy.yaml -f dev/kind/gitea.yaml` succeeds after substituting the placeholder
(`sed "s|__ALLOY_IMAGE__|grafana/alloy:v1.19.2|" dev/kind/alloy.yaml | kubectl create
--dry-run=client --validate=false -f -`); no file under `deploy/` touched.

**Check.**
```
. deploy/versions.env && sed "s|__ALLOY_IMAGE__|${ALLOY_IMAGE}|g" dev/kind/alloy.yaml | kubectl create --dry-run=client --validate=false -f - -o name
kubectl create --dry-run=client --validate=false -f dev/kind/gitea.yaml -o name
go test ./scripts/repocheck/ -count=1
```

**Live checks.** Three Alloy pods reach Running and the Collectors page shows three live
instances (`prod-eu-1/metrics`, `prod-eu-1/logs`, `staging-eu-1/metrics`); Gitea Ready, the
admin bootstrap succeeds once and reports "already exists" on the second `up`; the seed prints
`gitops: pushed demo-git.alloy to http://gitea:3000/shepherd-admin/shepherd-demo-config.git …`
the first time and `gitops: gitea-demo(exists)` the second; after `kubectl delete pod -l
app=gitea` the repo is still there (PVC).

### K5 — `gateway-oidc` · Gateway, mock OIDC and the two hand-written HTTPRoutes

**Files:** `dev/kind/gateway.yaml` (new), `dev/kind/oidc.yaml` (new), `dev/kind/routes.yaml`
(new), `scripts/repocheck/devkind_gateway_test.go` (new).

**Approach.**
- `gateway.yaml`: the §1 Gateway. Header: NGF provisions a per-Gateway nginx Deployment +
  Service in this namespace; the script pins its NodePort through the NGF chart values; the
  CoreDNS rewrite targets that Service — one rewrite for the whole zone, so pods and the browser
  see identical issuer strings on identical ports (D3).
- `oidc.yaml`: Deployment/Service `oidc` from §1 with the compose `JSON_CONFIG` verbatim,
  readiness `httpGet /default/.well-known/openid-configuration:8090`. Header: the pin stays here
  (D2), and the issuer is derived from the request Host.
- `routes.yaml`: two `HTTPRoute`s, `oidc` (hostname `oidc.localtest.me` → `oidc:8090`) and
  `gitea` (`gitea.localtest.me` → `gitea:3000`), both `parentRefs: [{name: dev}]`, PathPrefix `/`.

**Red run (write first).** `devkind_gateway_test.go` (yaml.v3 parsing):
(a) `gateway.yaml`: kind `Gateway`, name `dev`, `gatewayClassName: nginx`, exactly one listener
with `port: 80`, `protocol: HTTP`;
(b) `routes.yaml`: two HTTPRoutes; hostnames `oidc.localtest.me` / `gitea.localtest.me`;
backendRefs `oidc`/8090 and `gitea`/3000; every parentRef name is `dev`;
(c) `oidc.yaml`: image equals compose's `services.oidc.image`; env `SERVER_PORT` is `8090`;
`JSON_CONFIG` parses as JSON with `interactiveLogin: true`; a Service `oidc` on 8090;
(d) the shepherd HTTPRoute is **not** in these files (it is the chart's, K3) — assert no
hostname `shepherd.localtest.me` here.
Today: `open dev/kind/gateway.yaml: no such file or directory` (record).

**Acceptance.** Spec green; `kubectl create --dry-run=client --validate=false -f
dev/kind/gateway.yaml -f dev/kind/routes.yaml -f dev/kind/oidc.yaml` succeeds (Gateway API
kinds pass with `--validate=false` and no cluster).

**Check.**
```
kubectl create --dry-run=client --validate=false -f dev/kind/gateway.yaml -f dev/kind/routes.yaml -f dev/kind/oidc.yaml -o name
go test ./scripts/repocheck/ -count=1
```

**Live checks.** Gateway `Programmed=True`; `curl -s http://oidc.localtest.me/default/.well-known/openid-configuration | jq -r .issuer` prints `http://oidc.localtest.me/default` from the host **and** from a pod (`kubectl run … --image=curlimages/curl`); `http://gitea.localtest.me/` shows Gitea; HTTPRoutes report `Accepted=True`/`ResolvedRefs=True` for parent `dev`.

### K6 — `makefile` · the five targets

**Files:** `Makefile`, `scripts/repocheck/devkind_makefile_test.go` (new).

**Approach.** After the `dev-*` block (`Makefile:765-800`), with a design-note comment (why kind
beside compose, what is and is not shared, that `down` is the `dev-reset` equivalent, that
`config-scan` does not cover `dev/kind/` and why — C9):
```make
dev-kind: preflight-k8s docker-build-local docker-build-simulator ## Kind dev stack: cluster shepherd-dev with Calico, CNPG, Gateway API + NGF, the chart, Gitea, mock OIDC and three Alloy agents (http://shepherd.localtest.me)
	./scripts/dev-kind.sh up
dev-kind-reload: docker-build-local ## Rebuild shepherd:local, load it into shepherd-dev and roll the pods (migrations run first)
	./scripts/dev-kind.sh reload
dev-kind-seed: ## Re-run the dev seed inside shepherd-dev (idempotent)
	./scripts/dev-kind.sh seed
dev-kind-status: ## Show shepherd-dev's workloads, routes, DNS rewrite and URLs
	./scripts/dev-kind.sh status
dev-kind-down: ## Delete the shepherd-dev cluster and all its data (the dev-reset equivalent)
	./scripts/dev-kind.sh down
```
Add the five names to `.PHONY` (`Makefile:1`). `reload` builds only the app image (the
simulator changes rarely; `make dev-kind` rebuilds and reloads both and is idempotent). The
`help` env-knob list is unchanged. The Makefile already `include`s and `export`s versions.env
(`:12-13`), so the script inherits the pins when invoked through make and re-sources them when
run directly.

**Red run (write first).** `devkind_makefile_test.go`: for each of the five targets
`mkTargetLine` finds a `## ` help comment; `dev-kind` depends on `preflight-k8s`,
`docker-build-local` and `docker-build-simulator`; `dev-kind-reload` on `docker-build-local`;
every recipe invokes `./scripts/dev-kind.sh <verb>` with the matching verb; all five are in
`.PHONY` (pattern from `makefile_test.go:16-21`); `runMake("-n", "dev-kind-down")` prints the
script call and not "is up to date". Today: `target "dev-kind" not found in Makefile`.

**Acceptance.** Spec green; `go test ./scripts/repocheck/ -count=1` green (the guards spec
still finds exactly the ten `check-*` prerequisites); `make help` lists the five targets;
`make lint` green.

**Check.**
```
go test ./scripts/repocheck/ -count=1 && make help | grep -c '^  .*dev-kind' && make -n dev-kind-down
```

**Live checks.** `make dev-kind` end to end (§5); `make dev-kind-reload` rolls the pod through
the migrate hook (pod age resets, `dev-build` annotation changes); `make dev-kind-down` deletes
the cluster.

### K7 — `docs` · plan section, target tables, pin locations, ledger line

**Files:** `docs/kind-test-environment-plan.md`, `docs/dev-guide.md`, `README.md`,
`e2e/k8s/README.md`, `AGENTS.md`, `docs/project-status.md`, `CHANGELOG.md`.

**Approach.**
- `docs/kind-test-environment-plan.md`: new **§11 "Reusable dev stack (`make dev-kind`)"** —
  what it shares with the suite (Calico, NGF, CNPG, the pins), what it deliberately does not
  (one node, no per-feature namespaces, no teardown on failure), the routing/DNS design (D3) with
  the one-rewrite rationale, the OIDC walk and the `dialGuard` boundary (D4, §5 step 10), the
  `reload` mechanics (C3), where the manifests live and why (C9), and the "agents do not run
  kind" rule. Update the status header's pin sentence and §9 item 1 (the CNI is now a versions.env
  variable, not a hardcode).
- `docs/dev-guide.md`: a "Kubernetes flavour: `make dev-kind`" section after Mode B (URLs,
  credentials, seed parity, the OIDC walk in short, offline `/etc/hosts` note, `down` wipes
  data), and the five rows in the Dev stack target table (`:167-176`). Fix the stale sentence at
  `:126-128` to mention that the kind stack declares the mock issuer.
- `README.md`: one row in the command table (`:205-211`).
- `e2e/k8s/README.md`: `:51` now says the NGF chart version is `NGF_CHART_VERSION` in
  versions.env; `:45` mentions `KIND_NODE_IMAGE`/`CALICO_VERSION` there; "Known limits" first
  bullet: the CNI is a versions.env pin, still not a runtime parameter.
- `AGENTS.md`: Commands "Local dev stack" line gains `make dev-kind` / `dev-kind-reload` /
  `dev-kind-down`; the image table gains `kindest/node` (versions.env, tag only,
  Renovate-excluded) and `projectcalico/calico` manifest (`CALICO_VERSION`), and notes that
  `dev/kind/*.yaml` restates the Gitea and mock-oauth2 pins beside the compose files.
- `docs/project-status.md`: the document-map row for `kind-test-environment-plan.md` mentions
  §11 `make dev-kind`.
- `CHANGELOG.md` `## Unreleased`: one line under the developer-tooling heading the file uses.

**Red run.** Docs have no runtime control; the checks are the existing guards: `make
check-docs-drift`, `make check-docs-version` (both untouched by this slice — no
`scripts/docs-content` edit, no version quoted), and `go test ./scripts/repocheck/`.

**Acceptance.** `make guards` green; every command quoted in the docs exists in the Makefile
(`grep -o 'make dev-kind[a-z-]*' docs/dev-guide.md README.md | sort -u` ⊆ the five targets);
`git status --porcelain site/` empty.

**Check.**
```
make guards && go test ./scripts/repocheck/ -count=1
```

**Live checks.** None; the orchestrator re-reads §11 against what §5 actually showed and fixes
any sentence the cluster contradicted before merging.

## 3. Cross-slice seams the orchestrator re-checks after merge

- K1's script names manifests from K3/K4/K5 and pins from K2; each slice is self-consistent
  alone (its checks need none of the others), the whole works only together — expected.
- `make helm-lint` renders every `ci/` values file (`Makefile:700-710`) but not `dev/kind/values.yaml`;
  K3's spec covers it. At integration the orchestrator adds one line to `helm-lint`:
  `helm template shepherd deploy/helm/shepherd -f dev/kind/values.yaml > /dev/null` (Makefile
  is K6's file; adding it inside K6 would make K6 red until K3 lands).
- `dev/*.alloy` now feeds two stacks; nothing here edits them.
- `deploy/versions.env` in the PR path filters bills `e2e-k8s`, `e2e-sim` and `schema-verify`
  on the integration PR — expected, and the `e2e-k8s` run is the proof for K2.
- The guards job is always-on, so `dev/kind/*` edits on later PRs still run the new specs.

## 4. Integration order and the final gate on `feat/kind-dev-stack`

1. Merge in any order (file sets are disjoint). Then add the `helm-lint` line from §3.
2. Static gate from the repo root:
```
make lint
go test ./scripts/repocheck/ -count=1
go vet -tags e2ek8s ./e2e/k8s/
make helm-lint
bash -n scripts/dev-kind.sh && shellcheck scripts/dev-kind.sh
```
3. Live gate: §5. Only after it passes, open the PR; `e2e-k8s.yml` runs on it because
   `deploy/versions.env` changed.

## 5. Live bring-up and the OIDC walk (orchestrator only)

Prerequisites: Docker running, nothing else on host port 80, `kind`/`helm`/`kubectl` on PATH,
internet for the first pulls (Calico, NGF, CNPG, Gitea, mock-oauth2, Alloy).

```
git checkout feat/kind-dev-stack
make dev-kind                                   # ~6-10 min cold; idempotent — run twice
make dev-kind-status
```
Then, with `K="kubectl --context kind-shepherd-dev"`:
1. `$K get nodes` — one node Ready, image `KIND_NODE_IMAGE`; `$K -n kube-system get pods` —
   calico-node, calico-kube-controllers, coredns Running.
2. `$K -n shepherd-dev get pods` — `shepherd`, `shepherd-simulator`, `shepherd-db-1`, `gitea`,
   `oidc`, `alloy-metrics`, `alloy-logs`, `alloy-staging`, `dev-nginx-*` all Running; no
   `shepherd-migrate` pod (hook-succeeded deletes it — that is success).
3. `$K -n shepherd-dev get gateway dev -o jsonpath='{.status.conditions[?(@.type=="Programmed")].status}'` → `True`.
4. `$K -n shepherd-dev get httproute` — `shepherd`, `oidc`, `gitea`, each Accepted.
5. `$K -n kube-system get cm coredns -o jsonpath='{.data.Corefile}' | grep localtest` — exactly one rewrite line.
6. `$K -n shepherd-dev get svc -l gateway.networking.k8s.io/gateway-name=dev -o jsonpath='{.items[0].spec.ports[?(@.port==80)].nodePort}'` → `30080` (**the D3 live check**).
7. `curl -sf http://shepherd.localtest.me/healthz && curl -sf http://shepherd.localtest.me/readyz`;
   `curl -s http://oidc.localtest.me/default/.well-known/openid-configuration | jq -r .issuer` → `http://oidc.localtest.me/default`;
   `$K -n shepherd-dev run c --rm -i --restart=Never --image=curlimages/curl -- -s http://oidc.localtest.me/default/.well-known/openid-configuration | jq -r .issuer` → the same string (pod and browser agree).
8. `$K -n shepherd-dev logs deploy/shepherd | grep -i -E 'oidc|discover'` — provider discovered, no dial-guard refusal.
9. Browser `http://shepherd.localtest.me` — login page offers **both** the local form and the
   SSO button labelled `Mock SSO`. Sign in `admin / admin` first: Collectors shows three live
   instances; Git shows repo `shepherd-demo-config` with credential `gitea-demo`; Pipelines →
   `demo-visual` → Simulate → Sandbox run completes (the simulator's NetworkPolicy is enforced by
   Calico and the harness ports stay open). Sign out.
10. **OIDC walk.** Click the SSO button → the browser lands on
    `http://oidc.localtest.me/default/authorize?…` → mock-oauth2-server's interactive login page
    (two fields: **Username** and **Claims**, a JSON textarea; `interactiveLogin: true`).
    - **App admin**: Username `appadmin`, Claims
      `{"groups":["11111111-1111-1111-1111-111111111111"],"email":"appadmin@dev.local","name":"Dev App Admin"}`
      → Sign in → back on Shepherd as `Dev App Admin` with the Admin menu (`auth.go:484-486`
      matches the group against `SHEPHERD_AUTH_APP_ADMIN_GROUP_IDS`, `dev/shepherd.dev.env:15`;
      subject is `sub` = `appadmin` via the `generic` preset). `/api/me` shows `auth_method`
      `oidc` and that group.
    - **Org member (platform-org admin, not app admin)**: sign out, SSO again, Username
      `orgadmin`, Claims `{"groups":["22222222-aaaa-4000-8000-000000000001"],"email":"orgadmin@dev.local","name":"Dev Platform Admin"}`
      (`dev.go:41` `seedPlatformAdminGroupID`) → org switcher shows `platform-org` with admin
      rights, no Admin menu. Reader: `…-000000000002`; data-eng admin: `…-000000000003`.
    - **`/admin/auth` as the app admin**: the page shows the info banner *"This provider is
      configured by the Helm chart (oidc.issuer). Change it in your chart values; it cannot be
      edited here."* (`settings_admin.go:72`); every field read-only, Save and Test connection
      disabled (`AdminAuthPage.tsx:186,251`); the provider reads `generic` / `Mock SSO`, issuer
      `http://oidc.localtest.me/default`.
    - **Known limit, by design (D4)**: the SSO settings page cannot be exercised against this
      (or any) private issuer. If the chart issuer were removed and an admin pasted
      `http://oidc.localtest.me/default` and pressed Test connection, the answer is
      *"refusing to fetch http://oidc.localtest.me/default/.well-known/openid-configuration: it
      resolves to a private or loopback address, which an identity provider never does"*
      (`discovery.go:226-228`), because Test probes with `SourceDatabase` (`settings_admin.go:93`)
      and the name resolves to the NGF ClusterIP. Not a bug; do not "fix" it in this work.
11. `make dev-kind-seed` — every line reads `(exists)`/`skipped`-free; in particular
    `gitops: gitea-demo(exists)` and `seed complete`.
12. `make dev-kind-reload` — watch `$K -n shepherd-dev get pods -w`: a `shepherd-migrate-*` pod
    runs to Completed first, then a new `shepherd-*` pod replaces the old one;
    `$K -n shepherd-dev get deploy shepherd -o jsonpath='{.spec.template.metadata.annotations.dev-build}'`
    differs from before; `/healthz` stays 200 throughout.
13. `make dev-kind-down` → `kind get clusters` no longer lists `shepherd-dev`; `make e2e-k8s-clean`
    prints only `e2e-k8s-clean: done`.

Time-box: if any step fails twice, capture `kind export logs` and `$K -n shepherd-dev get
events --sort-by=.lastTimestamp` before the third attempt (AGENTS.md's three-rounds rule applies).

## 6. Blocked / out of scope (not slices)

- **`NOTES.txt` assumes https for a route hostname** (`templates/NOTES.txt:9-10`) — with
  `route.enabled` on an http Gateway it prints `https://shepherd.localtest.me`. Cosmetic, a chart
  template change (chart patch bump) — leave for the next chart release; the script's banner
  prints the real URL.
- **The SSO settings page against a private issuer** — a product boundary, not a defect (D4,
  §5 step 10). No change.
- **`localtest.me` needs public DNS on the developer's machine** — offline work requires
  `/etc/hosts` entries; documented in K7, no code.
- **No ask-first item is needed**: no new Go/npm dependency, no `proto/`, no RBAC-semantics or
  served-config change anywhere in this plan.
