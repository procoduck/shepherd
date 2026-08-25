import type { Route } from '@playwright/test';

export type Handler = (
  route: Route,
  params: Record<string, string>,
  state: MockState,
) => Promise<void> | void;

export interface MockState {
  me: unknown;
  orgs: unknown[];
  clusters: unknown[];
  collectors: unknown[];
  pipelines: unknown[];
  revisions: Map<string, unknown[]>;
  destinations: unknown[];
  gitCredentials: unknown[];
  repoLinks: unknown[];
  agentTokens: unknown[];
  assignments: unknown[];
  groupSearchResults: unknown[];
  auditRows: unknown[];
  attributes: Record<string, string[]>;
  previewResult: { count: number; collector_ids: string[] };
  validateResult: { valid: boolean; diagnostics: unknown[] };
  servedConfig: { content: string; hash: string };
  unmatched: string[];
  authMethods?: {
    oidc: boolean;
    local_admin: boolean;
    oidc_display_name?: string;
    oidc_provider?: string;
  };
  // Admin → Single sign-on. `oidcSettings` seeds GetOidcSettings/
  // UpdateOidcSettings (wire shape, snake_case like every other mock);
  // `oidcTestResult` seeds TestOidcSettings so a spec can drive both the
  // discovery-succeeded and discovery-failed branches without a live IdP.
  users?: Record<string, unknown>[];
  // Teams and their explicit members. Members are keyed by team id; a team
  // backed only by an IdP group simply has no entry, which is the state the
  // Teams page has to render distinctly from "this team is empty".
  teams?: Record<string, unknown>[];
  teamMembers?: Record<string, Record<string, unknown>[]>;
  oidcSettings?: Record<string, unknown>;
  oidcTestResult?: Record<string, unknown>;
  localAdminCreds?: { username: string; password: string };
  localAdminPersona?: import('../fixtures/personas').MeResponse | null;
  schema?: unknown; // schema payload for GET /api/schema/:version
  visualRenderResult?: unknown;
  graphViewResult?: unknown;
  upgradeCheckResult?: unknown;
  simulateRelabelResult?: unknown;
  simulateLogsResult?: unknown;
  // S3 sandbox run (VB-1 §6.4). `simulateRunResult` seeds the run's
  // TERMINAL fields (status/rewrites/captured_*/component_health/etc, see
  // handlers.ts's simulateRunToWire) — GetRun synthesizes queued/running
  // snapshots on the way there itself, keyed off `simulateRunPollCount`, so
  // a spec never has to fake wall-clock timing to observe the whole
  // validating -> transforming -> running -> results progression.
  simulateRunResult?: unknown;
  simulateRunPollCount?: number;
  simulateRunId?: string;
  simulateRunCreatedAt?: string;
  simulateRunStartedAt?: string;
  simulateCreateRunError?: { status: number; code: string; message: string };
}

export interface RouteEntry {
  method: string;
  pattern: RegExp;
  keys: string[];
  handler: Handler;
}

export class Router {
  private routes: RouteEntry[] = [];
  public state: MockState;

  constructor(state: MockState) {
    this.state = state;
  }

  register(method: string, path: string, handler: Handler) {
    const keys: string[] = [];
    const pattern = new RegExp(
      '^' +
        path.replace(/:([^/]+)/g, (_, k) => {
          keys.push(k);
          return '([^/]*)';
        }) +
        '$',
    );
    this.routes.push({ method: method.toUpperCase(), pattern, keys, handler });
  }

  /**
   * A copy of the current route table. A fixture that registers a wrapping
   * handler (delay, failNext) takes a snapshot first and delegates back into
   * it via handle(route, snapshot) — route.continue() would send the request
   * to the live vite preview, which has no backend.
   */
  snapshot(): RouteEntry[] {
    return [...this.routes];
  }

  async handle(route: Route, entries: RouteEntry[] = this.routes) {
    const req = route.request();
    const method = req.method().toUpperCase();
    const url = new URL(req.url());
    const path = url.pathname;

    for (const entry of [...entries].reverse()) {
      if (entry.method !== method && entry.method !== '*') continue;
      const m = path.match(entry.pattern);
      if (!m) continue;
      const params: Record<string, string> = {};
      entry.keys.forEach((k, i) => {
        params[k] = m[i + 1];
      });
      await entry.handler(route, params, this.state);
      return;
    }

    // Unmatched — record and return 599
    const key = `${method} ${path}`;
    this.state.unmatched.push(key);
    await route.fulfill({
      status: 599,
      contentType: 'application/json',
      body: JSON.stringify({ error: { code: 'unmatched_mock', message: `No handler for ${key}` } }),
    });
  }
}

// Helper to create a JSON fulfill response
export function json(route: Route, status: number, body: unknown) {
  return route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify(body),
  });
}

// Helper to fulfill a Connect RPC error. `code` is a Connect error code
// string (e.g. "not_found", "failed_precondition", "unavailable") — see
// @connectrpc/connect's Code enum / protocol-connect/code-string.ts. Any
// non-200 status is treated by the client as a Connect error response and
// its body parsed as {code, message}; the status itself only needs to be
// non-200 (it does not have to match the code's canonical HTTP mapping).
export function connectError(route: Route, status: number, code: string, message: string) {
  return route.fulfill({
    status,
    contentType: 'application/json',
    body: JSON.stringify({ code, message }),
  });
}

export function list<T>(items: T[], limit = 25, offset = 0) {
  const page = items.slice(offset, offset + limit);
  return { items: page, total: items.length };
}
