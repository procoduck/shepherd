import { defineConfig, devices } from '@playwright/test';

/**
 * Fullstack Playwright configuration.
 * Runs against a REAL backend (no network mocks) at http://localhost:8080.
 * The dev stack must be running before executing this suite — use `make test-fullstack`.
 *
 * Scope: UI-API contract correctness.
 * Never use page.route() interception in fullstack specs.
 */
export default defineConfig({
  testDir: './tests/fullstack',
  fullyParallel: false, // workers:1 — real shared DB
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 2 : 0,
  workers: 1,
  reporter: process.env.CI
    ? [['html', { open: 'never', outputFolder: 'playwright-report/fullstack' }], ['github']]
    : 'list',
  use: {
    baseURL: 'http://localhost:8080',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    colorScheme: 'dark',
    viewport: { width: 1440, height: 900 },
  },
  // No webServer block — the stack is managed by make test-fullstack.
  //
  // NOTE the same staleness trap as playwright.config.ts, one level further out: this
  // suite hits the Go server, which serves the SPA embedded at image-build time. A web
  // source change only reaches it after `make docker-build-local` and a container
  // recreate — `pnpm build` alone is not enough.
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
