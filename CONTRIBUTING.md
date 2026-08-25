# Contributing to Shepherd

Thanks for taking an interest. This document is the short version of how the
repository expects changes to arrive; `docs/spec.md` is the long version of what
the system is meant to do, and it is the source of truth when the two disagree.

## Getting a working environment

```bash
make dev          # Postgres, a seeded database, Shepherd, Gitea and three Alloy
                  # agents, in Docker. http://localhost:8080, sign in admin/admin
make dev-reset    # stop everything and wipe the volumes
```

`make dev` is the fastest way to see a change running. If you would rather run
the SPA with hot reload against that backend, `make dev-frontend`.

Building by hand needs Go (version per `go.mod`), Node 24 with pnpm, Docker, and
Helm. `make tools` installs the Go-side generators at their pinned versions.

## Before you open a pull request

```bash
make lint         # golangci-lint (v2 config) + the repo guards + helm lint
make test         # the whole Go suite; spins up real Postgres via testcontainers
cd web && pnpm ci # typecheck, vitest, biome, build
```

Two things catch people out:

- **Generated code is committed.** `gen/`, `web/src/gen/` and
  `internal/store/sqlc/` are produced by `make generate` (buf + sqlc) and CI
  fails if the committed output does not match a fresh regeneration. Change the
  `.proto` or the `.sql`, then regenerate — never hand-edit the output.
- **The built SPA is committed** to `internal/spa/dist/`, because the server
  embeds it. Any web change needs `./scripts/build-web.sh` and the result staged
  with it, or the `check-dist-consistency` guard fails.

## Tests

New behaviour needs a test that fails without the change. That is not a
formality here: several bugs in this repo's history shipped because a test
asserted the wrong layer — a metric existed rather than being observed, a row
reached the database rather than being readable through the API. Assert the
thing a user or operator would actually notice.

Integration specs are tagged `Label("integration")` and need Docker. The
Playwright suites are `pnpm test:ui` (mocked) and `make test-fullstack` (against
the real stack).

## Commits and pull requests

- Conventional-commit subjects (`feat(auth):`, `fix(ci):`, `docs:`, `chore:`).
- **Say why, not just what.** The commit log is the main design record for this
  project; a message explaining the reasoning behind a non-obvious choice is
  worth more than one restating the diff.
- One logical change per PR where you can manage it. CI runs only the jobs your
  paths can affect, so a focused PR is also a faster one.

## Security

Please do not open a public issue for a vulnerability. See
[SECURITY.md](SECURITY.md).

## Licence

By contributing you agree that your contributions are licensed under the
[Apache License 2.0](LICENSE).
