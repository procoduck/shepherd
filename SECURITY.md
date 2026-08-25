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
- The agent-facing `collector.v1` surface and the agent token verification.

## Not in scope

- Findings that require an already-compromised app-admin session, unless they
  cross a boundary an app admin is not meant to cross.
- Vulnerabilities in dependencies that are not reachable from any code path we
  build or ship. `govulncheck` is the arbiter; a reachable one is in scope.
- The `dev/` and `e2e/` stacks. They ship deliberately fake credentials
  (`admin`/`admin`, keys that decode to "not-a-real-secret") and are not
  intended to be deployed.
