# Shepherd

[![CI](https://github.com/procoduck/shepherd/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/procoduck/shepherd/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/procoduck/shepherd?sort=semver)](https://github.com/procoduck/shepherd/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/procoduck/shepherd)](go.mod)
[![Licence](https://img.shields.io/github/license/procoduck/shepherd)](LICENSE)

Self-hosted, Kubernetes-first fleet manager for **Grafana Alloy**.

Shepherd runs in your cluster and serves every Alloy collector the configuration
it should be running, over Alloy's own `remotecfg` protocol. Collectors poll it;
it never connects to them, so it needs no inbound path to a collector and no
access into the clusters it manages.

**[Documentation](https://procoduck.github.io/shepherd/docs/)** ·
[Quickstart](https://procoduck.github.io/shepherd/docs/quickstart.html) ·
[Helm values](https://procoduck.github.io/shepherd/docs/helm-values.html) ·
[Changelog](CHANGELOG.md)

![The visual pipeline builder: component palette, canvas, live validity and the generated Alloy config](site/img/visual-builder.jpg)

## What it does

- **Serves merged config.** Every enabled pipeline whose matchers select a
  collector is merged into the single config that collector receives. A
  collector that matches nothing gets an empty config, not an error, so it keeps
  running what it already had.
- **Refuses to serve config that would not run.** Three gates: syntax,
  `alloy validate`, and a merge dry-run against each affected collector's *full*
  merged config — so a pipeline that is valid alone but conflicts with an
  enabled one is caught before any agent sees it.
- **Three ways to author.** Guided wizards for six common jobs, a visual builder
  that generates Alloy as you wire components together, or raw Alloy pasted in.
  All three land in the same merge engine and the same gate.
- **GitOps from any git server.** Poll a repo and turn its files into validated
  pipelines. PAT, basic, SSH, Azure DevOps service principal, and GitHub App auth.
- **Sandbox simulation.** Run a candidate pipeline against synthetic telemetry in
  a network-isolated sandbox before it reaches a collector.
- **Multi-tenant by design.** Collectors are claimed into organisations, and
  roles are granted per organisation — from your identity provider's groups, from
  local accounts, or both.

It is **not** a telemetry backend. Shepherd never receives your metrics, logs or
traces; it configures the collectors that ship them.

## Requirements

| To | You need |
|---|---|
| Run Shepherd | A Kubernetes cluster, Helm 3, and a **PostgreSQL 14+** it can reach. The chart needs no CRDs by default. |
| Run collectors | [Grafana Alloy](https://grafana.com/docs/alloy/) **v1.18.1** — the version whose component schema this build validates against, pinned in `deploy/versions.env`. |
| Build from source | Go (see `go.mod`), Node 24 with pnpm, Docker (tests start real PostgreSQL via testcontainers), and Helm. |

Two optional integrations need their operators installed first:
[CloudNativePG](https://procoduck.github.io/shepherd/docs/database.html) to have
the database provisioned for you, and
[External Secrets](https://procoduck.github.io/shepherd/docs/secrets.html) to
have the encryption key and first password generated in-cluster.

## Install

The chart is published as an OCI artifact — nothing to clone, no repo to add.

```bash
kubectl create namespace shepherd

# Two values are required. Generate the encryption key ONCE and keep it: it
# encrypts git credentials and the OIDC client secret at rest, and a replacement
# key cannot decrypt what the old one wrote.
kubectl -n shepherd create secret generic shepherd-secrets \
  --from-literal=SHEPHERD_DATABASE_URL='postgres://user:pass@host:5432/shepherd' \
  --from-literal=SHEPHERD_SECURITY_ENCRYPTION_KEY="$(openssl rand -base64 32)" \
  --from-literal=SHEPHERD_BOOTSTRAP_ADMIN_PASSWORD='choose-a-password'

helm install shepherd oci://ghcr.io/procoduck/charts/shepherd --version 0.9.0 \
  --namespace shepherd --set existingSecret=shepherd-secrets

kubectl -n shepherd port-forward svc/shepherd 8080:8080
```

Sign in at <http://localhost:8080> as `admin` with the password you set. Nothing
is exposed outside the cluster until you ask for it — see
[Kubernetes](https://procoduck.github.io/shepherd/docs/kubernetes.html) for
ingress, upgrades and what the chart renders, and
[Configuration](https://procoduck.github.io/shepherd/docs/configuration.html)
for every setting.

> The `--version` above is the **chart's** version, which moves independently of
> Shepherd's. `helm show chart oci://ghcr.io/procoduck/charts/shepherd` reports
> which Shepherd release a given chart version installs.

### Just want to look at it?

`make dev` boots the whole thing in Docker — PostgreSQL, seeded data, a mock
identity provider, a git server, and live Alloy collectors already polling
Shepherd at <http://localhost:8080> with `admin` / `admin`. For trying Shepherd
or working on it, not for running it.

## Connect a collector

Mint an agent token at **Admin → Agent Tokens** (the secret is shown once), then
add a `remotecfg` block to your Alloy configuration:

```alloy
remotecfg {
  url = "http://shepherd.shepherd.svc.cluster.local:8080"
  basic_auth {
    username = "<token-uuid>"
    password = "<token-secret>"
  }
  attributes = {
    cluster = "prod-eu-1",
    role    = "metrics",
  }
  poll_frequency = "60s"
}
```

Those `attributes` are what pipeline matchers select on, so choose them
deliberately. Within one poll the collector appears under **Collectors**, and
its cluster can be claimed into an organisation.

## How it works

```
Alloy (spoke)  ──remotecfg──▶  Shepherd  ──serves──▶  merged Alloy config
                                  │
                 ┌────────────────┼────────────────────┐
                 ▼                ▼                    ▼
           PostgreSQL         Merge engine         Git (GitOps)
        (sessions, store)  (matcher + declare)      (repo_links)
```

Shepherd holds no state of its own — PostgreSQL is the only thing to back up,
and the chart runs two replicas by default. Every claimed collector is also
served a small **beacon** pipeline reporting which components it is running and
whether they are healthy: component names and health only, never config text or
raw samples. Set `server.beacon_disabled: true` to turn it off.

### Roles

A role is reached two ways — an identity provider group, or a local account —
and the two coexist. Shepherd runs with no identity provider at all.

| Role | Can |
|---|---|
| Application administrator | Everything, everywhere: organisations, cluster claiming, agent tokens, local users |
| Organisation administrator | Everything in one org, including what it *is*: destinations, routes, git credentials, teams |
| Organisation editor | Authors what the org *runs* — pipelines, wizards, simulations — but cannot change where telemetry ships |
| Team member | Writes only the pipelines their team owns (API only; the UI does not yet reflect team-scoped write) |
| Viewer | Read only |
| Service account | Machine access scoped `propose` (read and simulate) or `apply` (writes, acting for a named human) |

[Roles and access](https://procoduck.github.io/shepherd/docs/roles.html) covers
how each is assigned.

## Management API

The `/api/*` REST surface is a thin shim over a typed Connect RPC contract
(`shepherd.mgmt.v1`). Both share the same session-cookie authorization, and the
Connect endpoints are plain HTTP POST + JSON, so integrators may call them
directly — the tradeoff is camelCase fields (`orgId`) and
`shepherd.mgmt.v1.<Service>/<Method>` paths instead of REST resource paths.

```bash
curl -s -X POST http://localhost:8080/shepherd.mgmt.v1.PipelineService/ListPipelines \
  -H 'Content-Type: application/json' \
  -H 'X-Requested-With: XMLHttpRequest' \
  -b cookies.txt \
  -d '{"orgId":"<org-uuid>"}'
```

Agent tokens are scoped to the `collector.v1` fleet protocol only and cannot
reach this surface.

## Project status

Shepherd is in active development and pre-1.0; expect breaking changes, which
the [changelog](CHANGELOG.md) calls out explicitly.

Several subsystems are **built and tested but not wired to a running surface**:
a tenant-aware gateway and receiver tier, three-way reconciliation, onboarding
artifacts, a k8s-monitoring chart-values generator, and a read-plus-propose MCP
interface. `docs/gateway-tier-plan.md` §9 tracks what stands between each one
and being usable. Do not plan against them yet.

## Development

```bash
make dev        # the whole stack in Docker
make build      # SPA + Go binary
make test       # Go tests (needs Docker)
make lint       # golangci-lint plus the repo guards
make help       # every target, and the env knobs the suites honour
```

| Command | What it runs |
|---|---|
| `make e2e` | Compose end-to-end suite (~10 min, Docker) |
| `make e2e-k8s` | Kubernetes suite: a kind cluster, Gateway API, NGINX Gateway Fabric, CloudNativePG and External Secrets, then the chart (~12 min) |
| `make test-ui` | Mocked Playwright suite, no backend required |
| `make generate` | buf + sqlc + version codegen |
| `make docs` | Regenerate `site/docs/` from `scripts/docs-content/` |
| `make fmt` | `golangci-lint fmt` |

Generated code and the built SPA are committed and guarded by CI — see
[CONTRIBUTING.md](CONTRIBUTING.md), which covers the two things that catch
people out. `docs/spec.md` is the full specification.

## Contributing and support

Issues and pull requests are welcome; start with
[CONTRIBUTING.md](CONTRIBUTING.md). For anything security-related, follow
[SECURITY.md](SECURITY.md) rather than opening a public issue.

## Licence

[Apache License 2.0](LICENSE)
