# Shepherd — Frontend Testing with Playwright

Companion to `docs/spec.md`. This document specifies **two distinct Playwright layers**:

## Suite boundaries (the non-negotiable separation)

| Suite | Target | Command | Backend | Mocks |
|---|---|---|---|---|
| **Mocked suite** | `web/tests/specs/` | `make test-ui` | None — fully mocked | `page.route()` at network boundary only |
| **Fullstack suite** | `web/tests/fullstack/` | `make test-fullstack` | Real shepherd at `:8080` | NONE — never use `page.route()` here |

**Mocked suite scope:** UI behaviour, component contracts, loading/error/empty states, RBAC rendering, editor diagnostics, debounce, wizard multi-step. No real backend ever.

**Fullstack suite scope:** UI-API contract correctness. Tests that a real server rejects 401/403 correctly, that created entities persist across reload, that served-config contains the declare block after enable, that /api/me returns the canonical shape. No `page.route()` ever.

**Do not mix.** A fullstack spec that imports from `web/tests/fixtures/test.ts` (the mocked fixture) is wrong. A mocked spec that calls `page.request.post('/api/auth/local/login')` to get a real session is wrong.

**CI enforcement:** `grep -rn "page\.route" web/tests/fullstack/` → zero on every PR.

---

## Mocked suite principles (non-negotiable)

---

## 2. Layout & tooling

```
web/
├── playwright.config.ts
├── tests/
│   ├── fixtures/
│   │   ├── test.ts              # extended `test` with the `api` fixture (§5)
│   │   ├── factories.ts         # typed builders for every API resource (§4)
│   │   └── personas.ts          # appAdmin / orgAdmin / reader /me payloads (§6)
│   ├── mocks/
│   │   ├── router.ts            # route table + matcher (§5.1)
│   │   └── handlers.ts          # default handlers for the full §12 API surface
│   ├── helpers/
│   │   ├── editor.ts            # CodeMirror interaction helpers (§8)
│   │   └── nav.ts               # login-as + goto helpers
│   └── specs/
│       ├── auth.spec.ts
│       ├── overview.spec.ts
│       ├── collectors.spec.ts
│       ├── pipelines-list.spec.ts
│       ├── pipeline-editor.spec.ts
│       ├── editor-autocomplete.spec.ts
│       ├── wizard.spec.ts
│       ├── destinations.spec.ts
│       ├── git.spec.ts
│       ├── admin.spec.ts
│       ├── rbac.spec.ts
│       └── states.spec.ts       # loading / empty / error / toasts
```

- Dependencies (devDependencies in `web/package.json`): `@playwright/test` only. Do NOT add MSW — route interception is the single mocking mechanism (two mock systems drift apart).
- Scripts (run via pnpm): `"test:ui": "playwright test"`, `"test:ui:headed": "playwright test --headed"`, `"test:ui:report": "playwright show-report"`.
- Root `Makefile` gains `test-ui: cd web && pnpm build && pnpm exec playwright test`. CI runs it after `pnpm exec playwright install --with-deps chromium`.
- Add one line to `web/AGENTS.md`: `- UI tests: \`pnpm test:ui\` (Playwright, fully mocked backend — see docs/frontend-testing.md); single spec: \`pnpm exec playwright test tests/specs/<name>.spec.ts\``.
- This document is committed as `docs/frontend-testing.md`.

### 2.1 `playwright.config.ts`

```ts
import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./tests/specs",
  fullyParallel: true,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: process.env.CI ? 4 : undefined,
  reporter: process.env.CI ? [["html", { open: "never" }], ["github"]] : "list",
  use: {
    baseURL: "http://localhost:4173",
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    colorScheme: "dark",                    // spec §13.1: dark is default
    viewport: { width: 1440, height: 900 }, // above spec's 1280 minimum
  },
  webServer: {
    command: "pnpm preview --port 4173 --strictPort",
    url: "http://localhost:4173",
    reuseExistingServer: !process.env.CI,
  },
  projects: [{ name: "chromium", use: { ...devices["Desktop Chrome"] } }],
});
```

Tests run against the **production build** (`vite preview`), not the dev server — same bundle the Go binary embeds, so bundler-level differences can't hide. `make test-ui` builds first; locally, rebuild when app code changed.

---

## 3. Mock architecture

One route interception installed per test context, before any navigation:

```
page.route("**/{api,auth}/**", router)
        │
        ▼
  mocks/router.ts  — matches "METHOD /path/:params" patterns in registration order,
        │            later registrations override earlier (test-level > defaults)
        ▼
  mocks/handlers.ts — default handlers for EVERY endpoint in spec §12,
                      reading from an in-memory `MockState`
        │
        ▼
  unmatched → route.fulfill(status 599, body describing method+path)
              AND the api fixture records it → afterEach fails the test
```

`MockState` is a plain mutable object per test: `{ me, orgs, collectors, instances, pipelines, revisions, destinations, adoCredentials, repoLinks, agentTokens, auditRows, attributes }`. Default handlers implement realistic behavior against it: list endpoints honor `limit/offset` and return the `{items, total}` envelope; CRUD handlers mutate the state so a created pipeline appears in the next list response; enable/disable flips flags; `preview-matches` evaluates nothing — it returns whatever the test seeded (`state.previewResult`). Handlers replicate the API **envelope and shapes** from spec §12/§5 exactly, including error form `{"error": {"code", "message", "details"}}`.

Handlers must NOT reimplement backend business logic (matching, merging, validation). Where the UI needs a computed response, the test seeds it explicitly. This keeps mocks honest: they encode the *contract*, not a second implementation.

### 3.1 Contract drift guard

All mock payloads are typed with the same TypeScript types the app's `src/api/` client uses (import them — never redeclare). If the API types change, mocks fail to compile. Additionally, one Vitest test snapshots the route table against the endpoint list in `docs/spec.md` §12 (parse the code block) and fails when an endpoint exists in the spec but has no default handler.

---

## 4. Factories (`fixtures/factories.ts`)

Typed builder per resource with sequential IDs and sensible defaults; every field overridable:

```ts
export const collector = (o: Partial<Collector> = {}): Collector => ({
  id: seq("col"),                       // col-0001, col-0002 …
  cluster: "prod-eu-1",
  role: "metrics",
  org_id: "org-0001",
  instances_live: 2,
  versions: ["v1.12.2"],
  status: "APPLIED",
  last_seen: "2026-08-17T09:00:00Z",    // fixed — relative rendering tested with page.clock
  served_hash: "a1b2c3d4e5f6a7b8c9d0",
  ...o,
});
```

Provide: `org`, `collector`, `instance`, `pipeline` (default `source: "ui"`, one matcher `cluster="prod-eu-1"`), `revision`, `destination`, `adoCredential`, `repoLink`, `agentToken`, `auditRow`, `diagnostic` (`{line, col, message, stage}`), and `scenario.basic()` — a coherent MockState: 1 org, 4 collectors (one per role on `prod-eu-1`), 3 pipelines (ui enabled / wizard disabled / git), 2 destinations, 1 repo link. Most tests start from `scenario.basic()` and override.

---

## 5. The `api` fixture (`fixtures/test.ts`)

Extend Playwright's `test` with an `api` fixture exposing:

- `api.state` — the MockState (mutate before navigation to seed).
- `api.seed(partial)` — deep-merge into state.
- `api.loginAs(persona)` — sets `state.me` (§6). There is no cookie dance: the app only knows auth via `GET /api/me`; mocking it *is* logging in. Unauthenticated = handler returns 401, app must redirect to `/login`.
- `api.override(pattern, handler)` — per-test handler taking precedence over defaults. Handler receives `(route, params, state)` and may `route.fulfill` with anything, including errors and malformed bodies.
- `api.failNext(pattern, status = 500, error?)` — one-shot failure for retry/toast tests.
- `api.delay(pattern, ms)` — fulfill after a delay (pair with `page.clock`) for loading-state tests.
- `api.calls(pattern)` — recorded requests (method, path, query, parsed JSON body) for asserting *what the UI sent* — e.g. that Save posted the exact editor content, or that the validate endpoint was NOT called while typing inside the debounce window.
- Automatic `afterEach`: fail on any unmatched request; fail on any `console.error` from the page (allowlist: none — fix the app instead).

### 5.4 Settling helper

`await api.idle()` — resolves when no intercepted request has been in flight for 100ms. Use after actions that fan out queries; prefer plain locator assertions when a visible outcome exists.

---

## 6. Personas (`fixtures/personas.ts`)

Exact `/api/me` payloads matching the backend's response shape (spec §7.2 roles):

- `appAdmin` — `is_app_admin: true`, all orgs.
- `orgAdmin` — admin of `org-0001` only.
- `reader` — member of a group assigned to `col-0001` only.
- `nobody` — authenticated, zero grants.
- `unauthenticated` — handler returns 401.

RBAC spec (`rbac.spec.ts`) is a persona × surface matrix asserting spec §13.4's rule — write affordances are **hidden, not disabled**:

| Assertion | appAdmin | orgAdmin | reader |
|---|---|---|---|
| Admin nav group visible | yes | no | no |
| "New pipeline" button | yes | yes | no |
| Enabled switch on pipeline rows | yes | yes | absent |
| Editor Save button | yes | yes | absent (read-only banner behavior) |
| Collector Access tab | yes | yes | absent |
| Direct nav to `/admin/orgs` | renders | redirects/404 view | redirects/404 view |

---

## 7. Required scenarios per spec area

Each bullet is a test (or small group). Keep specs mapped 1:1 to spec §13.5 screens.

**auth.spec.ts** — unauthenticated visit to `/` lands on `/login` with the "Continue with Microsoft" button linking `/auth/login`; auth-failure query param renders the destructive Alert; authenticated visit to `/login` redirects to `/`.

**overview.spec.ts** — four stat cards show seeded numbers (`tabular-nums` values); Sync errors card red when > 0; "Needs attention" lists the seeded FAILED collector and shows the "All quiet" empty state when none; "Recent changes" renders 10 audit rows.

**collectors.spec.ts** — table columns per spec; role filter narrows rows AND writes `?role=metrics` to the URL; reloading with the param pre-applies the filter; 0-instance count renders amber; row click navigates to detail; detail tabs — Instances (unregistered row at reduced opacity + badge), Served Config (hash copy → clipboard + toast "Copied"), Pipelines (switch flips via mocked enable endpoint and refetches), Access (group search Combobox debounces 300ms — assert exactly one `groups/search` call via `api.calls` after typing "plat" quickly, using `page.clock`).

**pipelines-list.spec.ts** — matcher chips truncate at 3 with "+n"; source badges (ui/wizard/git) render; filters sync to URL; empty state shows both action buttons.

**pipeline-editor.spec.ts** —
- Load existing pipeline: left pane populated, editor shows content, "No problems" after initial validate.
- Typing pauses 800ms (advance `page.clock`) → exactly one validate call with the current content; seeded diagnostics render as squiggles + Problems panel rows; clicking a problem scrolls/focuses the position.
- Save success → toast, `api.calls` shows the full payload (name, matchers array, contents).
- Save with stage-3 failure (override save endpoint → 422 with per-collector details) → the "Validation failed on n collectors" Dialog with an Accordion per collector; "Back to editing" keeps state.
- Matcher builder: add row, pick key from mocked `/attributes`, preview card shows seeded "Matches n collectors"; n=0 renders amber caption.
- Revision select → diff (merge) view visible; "Restore this revision" posts and produces a new revision in state.
- Git-sourced pipeline: violet banner with repo path, everything read-only.

**editor-autocomplete.spec.ts** (see §8 for mechanics) —
- Top level: type `prom` → completion tooltip lists `prometheus.scrape` etc.; accept → snippet inserted with quoted label placeholder and required attrs.
- Inside `discovery.relabel` `rule` block: attribute completion offers `action`; after `action = ` the enum values appear; already-present attributes are filtered out.
- Export harvesting: with `discovery.relabel "annotated"` in the doc, typing inside `targets = ` offers `discovery.relabel.annotated.output`.
- No completion inside strings or comments.

**wizard.spec.ts** — full walk of the six steps with the exact labels; steps 3/4 struck-through when signal deselected; invalid namespace chip renders red; Continue disabled until zod passes; Review shows rendered configs from the mocked render endpoint with diagnostics; "Save & enable" calls commit and navigates to `/pipelines` with a toast.

**destinations.spec.ts / git.spec.ts / admin.spec.ts** — dialog validation per zod; ADO credential edit shows "Leave blank to keep current secret"; repo-link "Test & link" calls verify then create (assert order via `api.calls`); sync error dot + tooltip from seeded `sync_error`; cluster claim flow moves a row between the Unclaimed and Claimed cards; typed-confirm gating on Unclaim/Delete/Revoke (button disabled until exact name typed); agent-token create shows the one-time secret view with copyable YAML.

**states.spec.ts** — `api.delay` on a list → skeleton rows (never a spinner) then data; `api.failNext` on a list → inline destructive Alert with server message + Retry which refetches; mutation failure → error toast; theme toggle flips `<html>` class and persists across reload (localStorage); breadcrumbs match route.

---

## 8. CodeMirror interaction helpers (`helpers/editor.ts`)

CodeMirror is not a `<textarea>`; interact through the DOM it renders:

```ts
export const editor = (page: Page, nth = 0) => {
  const root = page.locator(".cm-editor").nth(nth);
  const content = root.locator(".cm-content");
  return {
    root, content,
    async focus() { await content.click(); },
    async type(text: string) { await this.focus(); await page.keyboard.type(text); },
    async setValue(text: string) {              // replace-all via keyboard, not DOM injection
      await this.focus();
      await page.keyboard.press("ControlOrMeta+a");
      await page.keyboard.type(text);
    },
    async openCompletion() { await page.keyboard.press("ControlOrMeta+Space"); },
    completions: page.locator(".cm-tooltip-autocomplete li"),
    async accept(label: string) {
      await page.locator(".cm-tooltip-autocomplete li", { hasText: label }).click();
    },
    diagnostics: root.locator(".cm-lintRange-error"),
    async text() { return (await content.innerText()).replace(/\u200b/g, ""); },
  };
};
```

Rules: assert completion visibility with `await expect(ed.completions).toContainText([...])` (auto-waits); never read `view.state` via `page.evaluate` except one allowed helper for `text()` fallback if `innerText` proves unstable (`// DECISION` it); the 800ms validate debounce and all debounced UI are driven with `page.clock.install()` + `page.clock.fastForward("801ms")` — never real waits.

---

## 9. What this layer deliberately does NOT test

Merge semantics, matcher evaluation, validation correctness, hashing, RBAC *enforcement*, Graph/ADO behavior — all backend, covered by Ginkgo + the §18 e2e stack. If a UI test seems to need real backend logic, the test is wrong: seed the outcome instead. Visual regression (screenshot diffing) is out of scope for v1 — the spec's fixed tokens plus these behavioral tests are the guardrail. `// DECISION` allowed to add `toHaveScreenshot` later behind a separate tag.

## 10. Definition of done

- `make test-ui` green from clean checkout (build → preview → tests), < 3 min on CI, retries ≤ 1, zero `waitForTimeout` occurrences (grep-enforced in CI).
- Every endpoint in spec §12 has a default handler; the drift-guard test passes.
- Every screen in spec §13.5 has at least one spec exercising its primary flow, its empty state, and one failure state.
- Suite passes with `workers: 4` (proves test isolation — each test owns its MockState).