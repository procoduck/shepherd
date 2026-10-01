/**
 * Fullstack test fixtures.
 * Provides real-backend login helpers and DB-backed utilities.
 *
 * NEVER import the mocked `api` fixture here.
 * NEVER intercept network requests (page-level route mocks) in fullstack specs.
 */
import { type APIResponse, test as base, expect, type Page } from '@playwright/test';

// Dev stack credentials (from dev/shepherd.dev.env)
export const DEV_ADMIN_USERNAME = 'admin';
export const DEV_ADMIN_PASSWORD = 'admin';
export const DEV_BASE_URL = 'http://localhost:8080';

// Local editor/viewer accounts seeded by `shepherd dev seed` on the platform
// org (internal/cli/dev.go's seedLocalUsers) — same login/password pair as
// the seed's seedEditorLogin/seedEditorPassword and
// seedViewerLogin/seedViewerPassword constants.
export const DEV_EDITOR = { username: 'editor', password: 'editor-dev-pass' };
export const DEV_VIEWER = { username: 'viewer', password: 'viewer-dev-pass' };

/**
 * loginAs performs a real POST /api/auth/local/login and waits for the
 * shepherd_session cookie to be set. Fast: ~1 round-trip, no browser redirect.
 *
 * Retries once on a 429 from internal/auth/login_throttle.go's per-login
 * bucket (burst 10, refill ~1/6s) — real, and shared across this whole
 * suite's single dev stack: with `workers: 1` and enough specs logging in as
 * the same username in a tight window (most fullstack specs use admin), the
 * bucket can be exhausted well before it refills. The server's own
 * Retry-After tells us exactly how long that takes; honoring it is the
 * correct response to a real rate limiter, not a flakiness workaround, and
 * a second 429 (or any non-200) still fails loudly.
 */
export async function loginAs(page: Page, username: string, password: string): Promise<void> {
  for (let attempt = 0; ; attempt++) {
    const resp = await page.request.post('/api/auth/local/login', {
      data: { username, password },
      headers: {
        'Content-Type': 'application/json',
        'X-Requested-With': 'XMLHttpRequest',
      },
    });
    if (resp.status() === 200) return;
    if (resp.status() === 429 && attempt === 0) {
      const retryAfterSeconds = Number(resp.headers()['retry-after']) || 6;
      await page.waitForTimeout(retryAfterSeconds * 1000 + 250);
      continue;
    }
    throw new Error(`loginAs failed: ${resp.status()} ${await resp.text()}`);
  }
}

/** loginAsAdmin is loginAs for the seeded bootstrap admin — kept as its own
 * export because every existing fullstack spec imports it by name. */
export async function loginAsAdmin(page: Page): Promise<void> {
  return loginAs(page, DEV_ADMIN_USERNAME, DEV_ADMIN_PASSWORD);
}

/** A shepherd.mgmt.v1 service name, e.g. 'PipelineService'. */
export type MgmtService =
  | 'MeService'
  | 'AdminService'
  | 'UserService'
  | 'FleetService'
  | 'PipelineService'
  | 'DestinationService'
  | 'GitOpsService'
  | 'WizardService'
  | 'VisualService'
  | 'SimulateService'
  | 'AuditService'
  | 'TenantRouteService'
  | 'TeamService'
  | 'ServiceAccountService';

/**
 * rpc calls a shepherd.mgmt.v1 procedure the way the SPA's own transport
 * does (web/src/api/transport.ts): a Connect unary POST of JSON to
 * /shepherd.mgmt.v1.<Service>/<Method>, with the X-Requested-With header the
 * server's CSRF middleware demands on every non-GET request. The session
 * cookie from loginAs rides along on page.request.
 *
 * The raw APIResponse is returned so callers can assert on the HTTP status
 * the Connect protocol maps each code to (200 success — creates included —
 * 401 unauthenticated, 403 permission_denied, 503 unavailable, ...). Bodies
 * are protojson: lowerCamelCase field names, zero values (false, 0, "",
 * empty lists) OMITTED, int64 as strings; an error body is
 * { code: "<snake_case code>", message }.
 */
export function rpc(
  page: Page,
  service: MgmtService,
  method: string,
  data: Record<string, unknown> = {},
  headers: Record<string, string> = {},
): Promise<APIResponse> {
  return page.request.post(`/shepherd.mgmt.v1.${service}/${method}`, {
    data,
    headers: {
      'Content-Type': 'application/json',
      'X-Requested-With': 'XMLHttpRequest',
      ...headers,
    },
  });
}

/** The fields of MeService.GetMe the fullstack specs read. `orgs` is absent
 * (protojson omits an empty list) when the caller belongs to no org. */
export interface Me {
  orgs?: Array<{ id: string; name: string; role?: string }>;
}

/** getMe returns the logged-in caller's MeService.GetMe response, failing
 * loudly on anything but 200. */
export async function getMe(page: Page): Promise<Required<Me>> {
  const resp = await rpc(page, 'MeService', 'GetMe');
  if (resp.status() !== 200) {
    throw new Error(`GetMe failed: ${resp.status()} ${await resp.text()}`);
  }
  const me = (await resp.json()) as Me;
  return { orgs: me.orgs ?? [] };
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
  // A non-200 (e.g. 401 for a rejected token) means the recompute was NOT
  // triggered, which defeats this helper's purpose — fail loudly.
  if (resp.status() !== 200) {
    throw new Error(`forceRecompute failed: ${resp.status()} ${await resp.text()}`);
  }
}

/** Fullstack test fixture type — same base as Playwright test, no mock api. */
export const test = base;
export { expect };
