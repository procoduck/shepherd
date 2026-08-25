## What and why

<!-- What changes, and the reasoning behind it. The commit log is this project's
     main design record, so "why" is the part worth writing. -->

## Testing

<!-- What you ran, and what a reviewer should run. New behaviour wants a test
     that fails without the change — asserting the thing a user or operator
     would actually notice, not just that the code was reached. -->

- [ ] `make lint`
- [ ] `make test`
- [ ] `cd web && pnpm ci` (if the SPA changed)
- [ ] `./scripts/build-web.sh` run and `internal/spa/dist/` staged (if the SPA changed)
- [ ] `make generate` run and output committed (if proto/ or SQL changed)
