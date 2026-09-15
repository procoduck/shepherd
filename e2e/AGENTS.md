# Shepherd E2E

Compose-based e2e suites (`//go:build e2e`) plus the kind-based Kubernetes suite in `k8s/`
(`//go:build e2ek8s`). Always run through make — the targets exist to stop you testing stale images.

## Commands
- `make e2e` — core flow (registration, pipelines, not_modified, validation gate, GitOps, RBAC, APPLIED round-trip; standalone: local-admin login, embedded SPA); excludes `sandbox-sim`
- `make e2e-sim` — S3 sandbox suite: containment probes first, then run-lifecycle specs
- `make e2e-egress` — the containment probes alone (fast local check)
- `make e2e-k8s` — kind cluster suite, nine `func Test*` features (`grep -h '^func Test' e2e/k8s/*_test.go | grep -vc TestMain`): CNI negative control, Helm install with defaults, Helm install with the simulator on, repeatable install, chart-provisioned CNPG/ESO dependencies, simulator containment probes, simulator containment kill probe, Gateway operator-owned attachment, Gateway route conformance/tenant isolation. Last full-cycle timing (`e2e/k8s/README.md`'s ~500s figure, recorded for six capability groups rather than by `func Test`, incl. cluster create/destroy) predates this nine-count and has not been re-measured since the suite grew — treat it as a stale floor, not a current number.
- Focused run against a running stack (`E2E_KEEP=1` first): `ginkgo --tags=e2e --focus "GitOps" ./e2e`

## Rules
- **Never run raw `ginkgo ./e2e` or `go test -tags e2ek8s` as the primary invocation.** The make
  targets depend on the image builds (`e2e`: local + init; `e2e-sim`/`e2e-egress`: all three;
  `e2e-k8s`: local + simulator — migrations are a chart hook), so the stack runs images built from
  THIS working tree. A raw run tests whatever stale image the daemon holds — a
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
- Compose suites: no `time.Sleep` in specs — use `Eventually` (bounded backoff inside helper retry
  loops, e.g. `gitea_helpers_test.go`, excepted). The k8s suite is plain `go test`: use
  `wait.For(...)` or a deadline-bounded poll loop.
- Cluster pins (kind node image, Calico, NGF, CNPG, ESO) live in `deploy/versions.env`, shared with
  `make dev-kind`; `E2E_K8S_NODE_IMAGE` overrides the node image for this suite only, and
  `renovate.json` excludes the kind tag — bumping them is a reviewed change.
- CI: `e2e.yml` runs the compose suite on push to main only (path-filtered); `e2e-k8s.yml` runs on
  PRs touching the chart, `e2e/k8s/**`, `deploy/Dockerfile*` or `deploy/versions.env`, plus a weekly
  cron — a versions.env pin bump therefore triggers both.

## Debugging
- `E2E_KEEP=1 make e2e` (or e2e-sim) leaves the compose stack running; tear down with
  `docker compose -f e2e/docker-compose.e2e.yaml down -v`.
- `E2E_K8S_KEEP=1` keeps the kind cluster AND namespaces; `E2E_K8S_ARTIFACTS=dir` saves per-feature
  failure logs; `E2E_K8S_ALLOW_STALE_IMAGES=1` skips the image-mtime hint. `make e2e-k8s-clean` removes
  `shepherd-e2e-*` clusters a SIGKILLed run left behind (the `shepherd-dev` cluster from `make dev-kind`
  is separate — `make dev-kind-down`). See `e2e/k8s/README.md`.
- E2E stack login: `admin` / `e2e-local-admin-pass` (the dev stack uses `admin` / `admin`).

## Fixtures
- `mockmsft/` — mock Entra/Graph server; tests inject state via `POST /__fixture` (see `e2e_test.go`).
- Gitea is the git server for GitOps scenarios; `gitea_helpers_test.go` owns repo/token/push helpers.
