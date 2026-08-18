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
  // No webServer block — the stack is managed by make test-fullstack
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
});
