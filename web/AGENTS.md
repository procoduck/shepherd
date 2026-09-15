# Shepherd Web

React 19 + TypeScript + Vite SPA, embedded into the Go binary via `go:embed`.

## Commands (run from `web/`)
- `pnpm dev` — Vite dev server (proxies `/api` and `/auth` to `:8080`)
- `pnpm build` — TypeScript check + Vite build → `../internal/spa/dist` (the embedded bundle; see Rules)
- `pnpm typecheck` — tsc --noEmit
- `pnpm check` — Biome lint + format (fix in place)
- `pnpm check:ci` — the same check WITHOUT writing; this is what CI runs
- `pnpm ci` — the whole CI web job in one command (typecheck + tests + check:ci + build); also `make web-ci` from the repo root
- `pnpm test` — Vitest unit tests
- `pnpm test:ui` — Playwright **mocked** suite (full network mock — no real backend). Scope: UI behaviour, component contracts.
- `pnpm exec playwright test --config playwright.fullstack.config.ts` — Playwright **fullstack** suite (real backend at :8080, no mocks). Run via `make test-fullstack`.
- Single spec: `pnpm exec playwright test tests/specs/<name>.spec.ts`

## Tooling
- **Package manager**: pnpm v11 via corepack; the pin is `package.json` `packageManager` (CI reads it) and `deploy/versions.env` PNPM_VERSION must match
- Activate on a new machine: `corepack prepare pnpm@11.22.0 --activate` (or `brew install pnpm` if your registry lacks v11)
- **Linter/formatter**: Biome (`biome.json`) — replaces ESLint + Prettier entirely
- **Build**: Vite 8 (rolldown bundler) with `@tailwindcss/vite` plugin. Heavy code is behind lazy boundaries — the visual builder and graph view as routes (`src/routes/router.tsx`), CodeMirror behind `src/editor/LazyAlloyEditor.tsx` — all through `src/lib/lazyNamed.ts`, which rejects readably when a chunk lacks its export. `chunkSizeWarningLimit` in `vite.config.ts` sits just above the measured entry so a new static import trips it
- **Registry**: public npm by default; configure a mirror in `web/.npmrc` if your organisation uses one
- **Lockfile**: `pnpm-lock.yaml` only — `scripts/repocheck` fails CI if `package-lock.json`, `yarn.lock` or `npm-shrinkwrap.json` is ever tracked

## Conventions
- Single quotes, 2-space indent, 100-char line width, LF — enforced by Biome
- No `any` in production code (warning); `any` allowed only in `*.test.ts(x)` (Vitest) — Playwright `*.spec.ts` gets the production warning
- Colours only via `src/index.css` `@theme` tokens (light overrides on `html.light` / `prefers-color-scheme`); `theme.test.ts` rejects raw `zinc-*` and phantom classes in pages/components/visual. Design tokens and screen specs: `docs/spec.md` §13
- `pnpm check` before committing (it is `biome check --write .`)
- The Shell (`web/src/components/Shell.tsx`, `flex h-screen overflow-hidden`) is a desktop-only
  layout — nothing collapses navigation or reflows for a small viewport. A couple of pages use
  Tailwind's `sm:`/`lg:` grid-column variants to add columns on a wider desktop window, but that
  is not mobile support. Do not add mobile specs or media-query work without a design decision.

## Rules
- **If a bug or failing test takes more than 3 rounds of attempts to fix, stop and get an independent adversarial review before continuing** — a fresh reviewer with no stake in the current theory. Give it the exact symptom, the failing code, everything already tried, and the exact error output. Act on its findings before making further changes.
- Always run `pnpm ci` before finishing a task — it is exactly the CI web job. `pnpm lint` (== `check:ci`, Biome's read-only check) skips typecheck, tests and the build, so a type error or a failing test can pass `pnpm lint` and still fail CI.
- `make test-ui` rebuilds `internal/spa/dist` via `scripts/build-web.sh`, kills whatever holds :4173, and the webServer builds again (`reuseExistingServer: false`) — no manual pkill needed.
- Single-spec runs rebuild too — `playwright.config.ts`'s webServer runs `pnpm run build` before `vite preview`, and `reuseExistingServer: false` means it always does. No manual `pnpm build` needed. The build writes to `../internal/spa/dist`; discard that after a test run (`git checkout -- internal/spa/dist && git clean -f internal/spa/dist/assets`) — the committed bundle is rebuilt deliberately at release time through `scripts/build-web.sh`.
- Suite layout: `tests/specs` (mocked, 50 files), `tests/fullstack` (real backend, never `page.route()`), `tests/mocks/{router,handlers}.ts`, `tests/fixtures/`. Mocked mutating handlers gate with `requireOrgRole`; fixtures stay snake_case and `handlers.ts` converts to wire camelCase. Details: `docs/frontend-testing.md`.
- Two guards fail the suite on its own shape: `persona-floor.spec.ts` (per-persona `loginAs` floors, appAdmin ≤ 60%) and `wait-budget.spec.ts` (`waitForTimeout` ≤ 25 total, `visual-*.spec.ts` only — currently at the cap, so poll a locator instead). Dialog input specs use `pressSequentially`, not `fill()`: `fill()` hides the Modal focus-steal class of bug.
- Visual builder state is the one non-Query store: zustand + zundo in `src/visual/store.ts` (undo/redo only through its actions); dirty = `docFingerprint(doc) !== savedFingerprint`, call `markSaved()` after a save.
- Canvas specs that drive React Flow with raw mouse events read positions through `tests/fixtures/canvas.ts` (`settledBox`, `waitForViewportSettled`): placing a node re-fits the viewport a tick later, and a drag built from a pre-fit box pans the canvas instead of moving the node.
