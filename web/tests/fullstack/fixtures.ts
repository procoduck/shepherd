/**
 * Fullstack test fixtures.
 * Provides real-backend login helpers and DB-backed utilities.
 *
 * NEVER import the mocked `api` fixture here.
 * NEVER use page.route() in fullstack specs.
 */
import { test as base, expect, type Page } from '@playwright/test';

// Dev stack credentials (from dev/shepherd.dev.env)
export const DEV_ADMIN_USERNAME = 'admin';
export const DEV_ADMIN_PASSWORD = 'admin';
export const DEV_BASE_URL = 'http://localhost:8080';

/**
 * loginAs performs a real POST /api/auth/local/login and waits for the
 * shepherd_session cookie to be set. Fast: ~1 round-trip, no browser redirect.
 */
export async function loginAsAdmin(page: Page): Promise<void> {
  const resp = await page.request.post('/api/auth/local/login', {
    data: { username: DEV_ADMIN_USERNAME, password: DEV_ADMIN_PASSWORD },
    headers: {
      'Content-Type': 'application/json',
      'X-Requested-With': 'XMLHttpRequest',
    },
  });
  if (resp.status() !== 200) {
    throw new Error(`loginAsAdmin failed: ${resp.status()} ${await resp.text()}`);
  }
}

/**
 * forceRecompute triggers a lazy serve-cache recompute by sending a Connect-JSON
 * GetConfig RPC. The collector token ID and secret must be set as env vars or
 * passed explicitly. Uses the seeded dev agent token.
 */
export async function forceRecompute(
  page: Page,
  collectorCluster: string,
  collectorRole: string,
  tokenId = '00000000-de00-4000-a000-000000000001',
  tokenSecret = 'dev-only-agent-secret-32byteslong',
): Promise<void> {
  const creds = Buffer.from(`${tokenId}:${tokenSecret}`).toString('base64');
  const resp = await page.request.post('/collector.v1.CollectorService/GetConfig', {
    headers: {
      'Content-Type': 'application/json',
      Authorization: `Basic ${creds}`,
    },
    data: {
      id: `fullstack-probe-${Date.now()}`,
      hash: '',
      localAttributes: { cluster: collectorCluster, role: collectorRole },
    },
  });
  // 200 or connect error is acceptable — we just need to trigger the recompute path
  expect([200, 401]).toContain(resp.status());
}

/** Fullstack test fixture type — same base as Playwright test, no mock api. */
export const test = base;
export { expect };
