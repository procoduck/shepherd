# Security policy

## Reporting a vulnerability

**Please do not report security issues in public GitHub issues.**

Use GitHub's private vulnerability reporting on this repository
(Security → Report a vulnerability), which opens a private advisory visible only
to the maintainers.

Please include what you can: affected version or commit, a description of the
issue, and the steps or a proof of concept that shows the impact. If you are not
sure whether something counts, report it anyway.

## What to expect

This is a small project, so the honest answer is best-effort rather than a
contractual SLA: an acknowledgement within a few days, an assessment of severity
and affected versions after that, and a fix released for the current version.
You will be credited in the release notes unless you would rather not be.

## Scope

Shepherd holds credentials and generates the configuration a fleet of collectors
runs, so the areas most worth attention are:

- Authentication and session handling (`internal/auth`), including the OIDC
  flows, local user accounts, and password handling.
- Authorization — the role decisions in `internal/auth/authz.go` and the Connect
  interceptor tables in `internal/mgmtapi`.
- Secret handling: git credentials and the OIDC client secret are encrypted at
  rest with the key in `security.encryption_key`.
- Anything that makes the server fetch a URL a user supplied. OIDC discovery is
  deliberately constrained (`internal/auth/discovery.go`); a way around those
  constraints is a finding.
- The agent-facing `collector.v1` surface and the agent token verification
  (`internal/agentapi/auth.go`, a Connect request gate that runs before the body is decoded;
  the service-account gate in `internal/mgmtapi/machine_auth.go` is the same shape).

## How the code is scanned

Automated, in the repository and on GitHub, so a reporter can see what is already covered:

- **Reachable Go vulnerabilities** — `govulncheck` in CI on every backend change and weekly
  (`govulncheck.yml`); SECURITY.md's arbiter for what counts as reachable.
- **Static analysis** — CodeQL (default setup, security-extended suite) on every PR and weekly;
  `gosec` inside `golangci-lint` on every lint run.
- **Container images** — Trivy over the built `shepherd` and `shepherd-simulator` images
  (`security-scan.yml`, and as a gate in `release.yml` before anything is published), plus a
  weekly scan of the images the last release shipped. The gate covers Shepherd's own binary and
  the distroless base; the bundled Grafana Alloy binary is reported but not gated, because its
  fixes arrive as an upstream Alloy version bump.
- **Infrastructure configuration** — Trivy misconfiguration checks over every Dockerfile and the
  rendered Helm chart; accepted findings are listed in `.trivyignore` with a reason each.
- **Secrets** — GitHub secret scanning with push protection, and `gitleaks` over the full git
  history on every PR (`make secrets-scan` locally).
- **Dependencies** — Dependabot version and security updates for Go, npm and GitHub Actions,
  weekly; Renovate for container images, which are pinned by digest as well as tag in
  `deploy/versions.env` and every file that restates them — except the kind node image, which
  is deliberately tag-only and excluded from Renovate (a bump must stay inside kind's and the
  gateway controller's supported range).
- **Supply chain** — every GitHub Action is pinned to a commit SHA (enforced by
  `scripts/repocheck`), release archives carry SBOMs, and release archives and images carry
  Sigstore provenance attestations (`gh attestation verify`). OpenSSF Scorecard runs weekly.
- **Branch protection** — `main` requires a pull request and the CI checks (guards, lint, build,
  test, web, test-ui, test-fullstack, CodeQL) for everyone, admins included.

## Not in scope

- Findings that require an already-compromised app-admin session, unless they
  cross a boundary an app admin is not meant to cross.
- Vulnerabilities in dependencies that are not reachable from any code path we
  build or ship. `govulncheck` is the arbiter; a reachable one is in scope.
  It runs on every backend-touching push/PR (CI's `build` job, `make
  vulncheck`) and again every week regardless of code changes (`.github/
  workflows/govulncheck.yml`, to catch a CVE freshly disclosed against a
  dependency already pinned here).
- The `dev/` and `e2e/` stacks. They ship deliberately fake credentials
  (`admin`/`admin`, keys that decode to "not-a-real-secret") and are not
  intended to be deployed.
