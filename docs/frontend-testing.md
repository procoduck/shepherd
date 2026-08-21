# Shepherd — Frontend Testing with Playwright

Companion to `docs/spec.md`. This document **describes what exists**; scenarios that were
specified but never built live in the explicit [Backlog](#backlog-not-yet-built) at the end.

## 1. Suite boundaries (the non-negotiable separation)

| Suite | Target | Command | Backend | Mocks |
|---|---|---|---|---|
| **Mocked suite** | `web/tests/specs/` | `make test-ui` | None — fully mocked | `page.route()` at network boundary only |
| **Fullstack suite** | `web/tests/fullstack/` | `make test-fullstack` | Real shepherd at `:8080` | NONE — never use `page.route()` here |

**Mocked suite scope:** UI behaviour, component contracts, loading/error/empty states, RBAC
rendering, editor diagnostics, debounce, wizard multi-step, the whole visual builder. No real
backend ever.

**Fullstack suite scope:** UI-API contract correctness. Tests that a real server rejects 401/403
correctly, that created entities persist across reload, that served-config contains the declare
block after enable, that /api/me returns the canonical shape. No `page.route()` ever.

**Do not mix.** A fullstack spec that imports from `web/tests/fixtures/test.ts` (the mocked
fixture) is wrong. A mocked spec that calls `page.request.post('/api/auth/local/login')` to get a
real session is wrong.

**Enforcement, honestly stated:**

- The fullstack `page.route()` ban is machine-enforced: the `check-no-route-mocks` guard (a
  `make lint` prerequisite, run by CI's guards job on every PR) greps `web/tests/fullstack/` for
  `page.route(` and fails on any non-comment hit.
- `waitForTimeout` is a **convention, not a CI gate**. Prefer `page.clock` for debounce and
  loading-state timing, and locator auto-waiting everywhere else. The exemption is real: the
  visual-builder canvas specs simulate mouse drags and React Flow interactions that `page.clock`
  cannot drive, so short real waits appear in those helpers deliberately. Do not add real waits
  outside that class of interaction.

---

## 2. Layout & tooling

```
web/
├── playwright.config.ts             # mocked suite
├── playwright.fullstack.config.ts   # fullstack suite
├── tests/
│   ├── fixtures/
│   │   ├── test.ts          # extended `test` with the `api` fixture (§5)
│   │   ├── factories.ts     # fixture builders + basicScenario() (§4)
│   │   ├── personas.ts      # appAdmin / orgAdmin / reader / nobody / localAdmin (§6)
│   │   └── schema-fixture.ts
│   ├── mocks/
│   │   ├── router.ts        # MockState + route matcher (§3)
│   │   └── handlers.ts      # default handlers + fixture→wire converters
│   ├── fullstack/           # real-backend specs + fixtures.ts (own fixture file)
│   └── specs/               # the mocked suite
```

`tests/specs/` holds 30+ spec files. By group rather than exhaustively:

- **Screens** — auth, local-login, overview, collectors, collector-access, pipelines-list,
  pipeline-editor, editor-autocomplete, revisions, served-config, wizard, destinations, git,
  git-page, admin, audit, org-switcher, rbac, states (loading/empty/error/toasts)
- **Visual builder** — `visual-*.spec.ts`: canvas, linking, inspector, code-sync, layout,
  selection-delete, drag-highlight, disable, graph-view, toolbar-save, upgrade, simulate-s2,
  simulate-s3
- **Cross-cutting** — a11y

Dependencies: `@playwright/test` only. No MSW — route interception is the single mocking
mechanism (two mock systems drift apart). Scripts: `pnpm test:ui` runs the mocked suite;
CI runs `make test-ui` after `pnpm exec playwright install --with-deps chromium`.

### 2.1 `playwright.config.ts` (the real one — summarized, not duplicated)

The mocked suite runs against the **production build** (`vite preview`), not the dev server —
the same bundle the Go binary embeds. Load-bearing settings in `web/playwright.config.ts`:

- `webServer.command: 'pnpm run build && pnpm exec vite preview --port 4173 --strictPort'` —
  `vite preview` serves whatever is in `dist/` and does NOT build, so the config always builds
  first; running preview alone silently tests a stale bundle.
- `reuseExistingServer: false` — a leftover preview from another run would also be stale.
  `make test-ui` kills stale previews before starting.
- `colorScheme: 'dark'` (spec §13.1 default), viewport 1440×900, retries 1 on CI, workers 4 on CI.

Single-spec runs (`pnpm exec playwright test tests/specs/<name>.spec.ts`) DO rebuild via the
webServer command, but see `web/AGENTS.md` for the caveats.

---

## 3. Mock architecture

One route interception installed per test context, before any navigation. The intercepted
surface is `/api/*`, `/auth/*`, **and** every `shepherd.mgmt.v1` Connect procedure POST
(`/shepherd.mgmt.v1.<Service>/<Method>`) — the app's primary API is the Connect contract, with
a few surviving REST routes.

```
page.route(matcher)
        │
        ▼
  mocks/router.ts  — matches "METHOD /path/:params" entries in reverse registration
        │            order, so later registrations override earlier ones
        │            (test-level api.override > defaults)
        ▼
  mocks/handlers.ts — default handlers for the API surface, reading from an
        │             in-memory MockState; fixture→wire converters map the
        │             snake_case fixture shapes to Connect camelCase
        ▼
  unmatched → route.fulfill(status 599, body naming method+path)
              AND recorded in state.unmatched → the fixture's afterEach fails the test
```

`MockState` (defined in `tests/mocks/router.ts`) is a plain mutable object per test. Its actual
fields: `me, orgs, clusters, collectors, pipelines, revisions, destinations, gitCredentials,
repoLinks, agentTokens, assignments, groupSearchResults, auditRows, attributes, previewResult,
validateResult, servedConfig, unmatched`, plus auth fields (`authMethods, localAdminCreds,
localAdminPersona`) and the visual/simulate family (`schema, visualRenderResult,
graphViewResult, upgradeCheckResult, simulateRelabelResult, simulateLogsResult,
simulateRunResult` and its poll bookkeeping — GetRun synthesizes the queued→running→completed
progression from `simulateRunPollCount` so specs never fake wall-clock timing).

Handlers must NOT reimplement backend business logic (matching, merging, validation). Where the
UI needs a computed response, the test seeds it explicitly (`previewResult`, `validateResult`,
`simulateRunResult`, …). This keeps mocks honest: they encode the *contract*, not a second
implementation.

### 3.1 Contract drift guard

The drift guard is **`web/src/routeCoverage.test.ts`** (Vitest), not compile-time typing:

- It parses the endpoint list out of `docs/spec.md` §12 (with a parser sanity check so it cannot
  pass vacuously if the heading or fence moves), maps each endpoint to its Connect procedure via
  an explicit `SPEC_TO_PROCEDURE` table, and fails when an endpoint has no default handler.
- Known, deliberate gaps live in `KNOWN_GAPS`, each citing the `docs/project-status.md` ledger
  item that owns closing it.

The fixture shapes in `tests/fixtures/factories.ts` **deliberately redeclare** snake_case
interfaces rather than importing the generated Connect types: they model the pre-migration
domain shape, and `handlers.ts`'s wire-shape converters translate fixture → camelCase wire.
So there is no "mocks fail to compile when API types change" guarantee — the route-coverage
test and the fullstack suite are the drift controls.

---

## 4. Factories (`fixtures/factories.ts`)

Builders with sensible defaults and every field overridable, plus `basicScenario()` — a coherent
starting MockState (org, collectors, pipelines, destinations, git fixtures). Most tests start
from `basicScenario()` and override. Shapes are snake_case fixture shapes (§3.1).

---

## 5. The `api` fixture (`fixtures/test.ts`)

Extends Playwright's `test` with an `api` fixture:

- `api.state` — the MockState (mutate before navigation to seed).
- `api.seed(partial)` — shallow-merge into state.
- `api.loginAs(persona)` — sets `state.me` and registers an init script exposing
  `window.__initialMe` before React mounts, so org-scoped queries can fire on first render.
- `api.override(method, path, handler)` — per-test handler taking precedence over defaults.
  Handler receives `(route, params, state)` and may fulfill with anything.
- `api.failNext(method, path, status?, error?)` — fails the initial request **plus one retry**
  (so `isError` sticks), then delegates onward. Emits the Connect `{code, message}` error shape
  for `/shepherd.mgmt.v1.*` paths and the legacy `{error: {code, message}}` envelope for REST.
- `api.delay(method, path, ms)` — fulfills after a real delay, then **delegates to the
  previously-registered handler** (default or override) rather than letting the request escape
  to the network — there is no backend in this suite to escape to.
- `api.calls(pattern)` — recorded requests (method, path, parsed JSON body) for asserting what
  the UI sent — e.g. that Save posted the exact editor content, or that validate was NOT called
  inside the debounce window.
- `api.idle()` — best-effort settle via `waitForLoadState('networkidle')`. Prefer plain locator
  assertions when a visible outcome exists.
- Automatic afterEach: fail on any unmatched request (the 599 path); fail on unexpected
  `console.error` from the page. The allowlist is deliberately narrow: any console line
  mentioning 401/Unauthorized (auth churn is inherent to the unauthenticated tests), and
  "Failed to load resource" messages **only** when both the status and the resource's
  pathname match an error a mock handler intentionally fulfilled in that test (tracked
  per-test via `injectedErrors`) — any other failed load or console error still fails the
  test. Do not widen it.

---

## 6. Personas (`fixtures/personas.ts`)

Exact `/api/me` payloads (spec §7.2 roles): `appAdmin` (all orgs), `orgAdmin` (admin of one org),
`reader`, `nobody` (authenticated, zero grants), `localAdmin`. Unauthenticated is expressed by
the `/api/me` handler returning 401. RBAC rendering follows spec §13.4's rule — write
affordances are **hidden, not disabled** — and `rbac.spec.ts` asserts both directions: presence
for privileged personas and absence (negative assertions) for the reader persona.

---

## 7. CodeMirror interaction

CodeMirror is not a `<textarea>`; specs interact through the DOM it renders (`.cm-editor`,
`.cm-content`, `.cm-tooltip-autocomplete li`, `.cm-lintRange-error`). One binding rule learned
the hard way (header comment in `editor-autocomplete.spec.ts`): completion is opened with
**`Control+Space` on every platform** — `ControlOrMeta+Space` resolves to Cmd-Space on macOS,
which is Spotlight, so the tooltip never opens locally while CI's Linux runner passes.

---

## 8. What this layer deliberately does NOT test

Merge semantics, matcher evaluation, validation correctness, hashing, RBAC *enforcement*,
Graph/git behavior — all backend, covered by Ginkgo + the e2e stack. If a UI test seems to need
real backend logic, the test is wrong: seed the outcome instead. There are no snapshot or
screenshot tests; behavioral assertions are the guardrail.

---

## Backlog (not yet built)

Specified in earlier revisions of this document but not implemented. Treat as candidate work,
not as coverage:

- **RBAC matrix completion** — reader-persona negatives now exist in `rbac.spec.ts` (no
  New-pipeline/Visual-builder/Enable/Save affordances, each with a positive control against
  vacuous passes); still missing: direct-nav denial (`/admin/orgs` for non-appAdmin personas),
  the collector Access tab per persona, and any use of the `nobody` persona.
- **Collectors screen depth** — role filter narrowing + `?role=` URL sync, clipboard copy toast,
  the 300ms group-search debounce asserted via `api.calls` call-count.
- **Pipelines list** — matcher-chip truncation ("+n"), source badges, filter↔URL sync.
- **Loading skeletons** — `states.spec.ts` covers error/empty/toast states but not
  skeleton-then-data via a delayed list response.
- **Wizard validation gating** — zod-gated Continue buttons, struck-through steps on signal
  deselection, Review-step render diagnostics.
- **Editor diagnostics depth** — the message text and "No problems"-absent assertions exist
  (`pipeline-editor.spec.ts`); still missing: the `.cm-lintRange-error` decoration and
  click-a-problem → cursor placement.
- **Autocomplete negative cases** — the comment context is pinned deterministic
  (`editor-autocomplete.spec.ts`); the string-literal context is not.
