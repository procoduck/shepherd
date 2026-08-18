# Shepherd Web

React 18 + TypeScript + Vite SPA, embedded into the Go binary via `go:embed`.

## Commands (run from `web/`)
- `pnpm dev` — Vite dev server (proxy `/api` to `:8080`)
- `pnpm build` — TypeScript check + Vite build → `dist/`
- `pnpm typecheck` — tsc --noEmit
- `pnpm check` — Biome lint + format (fix in place)
- `pnpm test` — Vitest unit tests
- `pnpm test:ui` — Playwright **mocked** suite (full network mock — no real backend). Scope: UI behaviour, component contracts.
- `pnpm exec playwright test --config playwright.fullstack.config.ts` — Playwright **fullstack** suite (real backend at :8080, no mocks). Run via `make test-fullstack`.
- Single spec: `pnpm exec playwright test tests/specs/<name>.spec.ts`

## Tooling
- **Package manager**: pnpm v11 (via corepack — see activation note below)
- **Activating pnpm v11 on a new machine:**
  ```sh
  corepack prepare pnpm@11.22.0 --activate
  ```
  If your registry doesn't have v11, use the brew install (`brew install pnpm`) and add `~/bin/pnpm` wrapper pointing to `/opt/homebrew/bin/pnpm`.
- **Linter/formatter**: Biome (`biome.json`) — replaces ESLint + Prettier entirely
- **Build**: Vite 6 with `@tailwindcss/vite` plugin
- **Registry**: public npm by default; configure a mirror in `web/.npmrc` if your organisation uses one

## Conventions
- Single quotes, 2-space indent, 100-char line width, LF — enforced by Biome
- No `any` in production code (warning); `any` allowed in test files
- All imports organised by Biome assist (auto on save)
- `pnpm check --write .` before committing

## Rules
- **If a bug or failing test takes more than 3 rounds of attempts to fix, stop and invoke the `adversarial-reviewer` subagent before continuing.** Describe the exact symptom, the failing code, what you have already tried, and the exact error output. Act on the reviewer's findings before making further changes.
- Always run `pnpm typecheck` and `pnpm exec biome check` on changed files before finishing a task.
- make test-ui always rebuilds dist/ then kills any stale vite preview (reuseExistingServer=false) — no manual pkill needed.
- Single-spec runs (`pnpm exec playwright test tests/specs/<name>.spec.ts`) do NOT rebuild — run `pnpm build` first.
