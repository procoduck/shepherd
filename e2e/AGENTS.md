# Shepherd E2E

Compose-based e2e suites (`//go:build e2e`) plus the kind-based Kubernetes suite in `k8s/`
(`//go:build e2ek8s`). Always run through make — the targets exist to stop you testing stale images.

## Commands
- `make e2e` — core flow (registration, pipelines, GitOps, RBAC); excludes `sandbox-sim`
- `make e2e-sim` — S3 sandbox suite: containment probes first, then run-lifecycle specs
- `make e2e-egress` — the containment probes alone (fast local check)
- `make e2e-k8s` — kind cluster suite (Helm install, NetworkPolicy enforcement); ~200s incl. cluster create/destroy
- Focused run against a running stack (`E2E_KEEP=1` first): `ginkgo --tags=e2e --focus "GitOps" ./e2e`

## Rules
- **Never run raw `ginkgo ./e2e` or `go test -tags e2ek8s` as the primary invocation.** The make
  targets depend on `docker-build-local` / `docker-build-init` / `docker-build-simulator`, so the stack
  runs images built from THIS working tree. A raw run tests whatever stale image the daemon holds — a
  source fix appears not to work, or a regression passes. (The k8s suite warns on image-mtime skew, but
  that is a hint, not a guard.)
- **Labels are load-bearing.** Every S3 spec carries `sandbox-sim` (this is what `make e2e`'s
  `!sandbox-sim` filter excludes, since its stack boots without the simulator); the reachability
  probes — THE control bounding what a sandbox run can reach — additionally carry `sandbox-egress`,
  so an egress probe always has BOTH labels. `make e2e-sim` runs the egress pass with
  `--fail-on-empty` so a typoed or deleted label fails the build instead of ginkgo reporting
  "Ran 0 of N Specs" and exiting 0. Keep new S3 specs inside this taxonomy.
- **Ordered flow, independent scenarios.** The main suite is one `Ordered` Describe; scenarios must not
  depend on each other beyond that documented ordering (a failed earlier step skips dependents).
  Standalone Describes (e.g. auth) must not reuse state (orgID etc.) from the ordered flow.
- No `time.Sleep` in specs — use `Eventually`. (Bounded backoff inside helper retry loops, e.g.
  `gitea_helpers_test.go`, is the one exemption.)

## Debugging
- `E2E_KEEP=1 make e2e` (or e2e-sim) leaves the compose stack running; tear down with
  `docker compose -f e2e/docker-compose.e2e.yaml down -v`.
- `E2E_K8S_KEEP=1` keeps the kind cluster AND namespaces; `make e2e-k8s-clean` removes clusters a
  SIGKILLed run left behind (the one path no in-process teardown covers). See `e2e/k8s/README.md`.
- E2E stack login: `admin` / `e2e-local-admin-pass` (the dev stack uses `admin` / `admin`).

## Fixtures
- `mockmsft/` — mock Entra/Graph server; tests inject state via `POST /__fixture` (see `e2e_test.go`).
- Gitea is the git server for GitOps scenarios; `gitea_helpers_test.go` owns repo/token/push helpers.
