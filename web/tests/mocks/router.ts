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
  adoCredentials: unknown[];
  repoLinks: unknown[];
  agentTokens: unknown[];
  auditRows: unknown[];
  attributes: Record<string, string[]>;
  previewResult: { count: number; collector_ids: string[] };
  validateResult: { valid: boolean; diagnostics: unknown[] };
  servedConfig: { content: string; hash: string };
  unmatched: string[];
  authMethods?: { oidc: boolean; local_admin: boolean };
  localAdminCreds?: { username: string; password: string };
  localAdminPersona?: import('../fixtures/personas').MeResponse | null;
  schema?: unknown; // schema payload for GET /api/schema/:version
  visualRenderResult?: unknown;
  graphViewResult?: unknown;
  upgradeCheckResult?: unknown;
  simulateRelabelResult?: unknown;
  simulateLogsResult?: unknown;
}

interface RouteEntry {
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

  async handle(route: Route) {
    const req = route.request();
    const method = req.method().toUpperCase();
    const url = new URL(req.url());
    const path = url.pathname;

    for (const entry of [...this.routes].reverse()) {
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

export function list<T>(items: T[], limit = 25, offset = 0) {
  const page = items.slice(offset, offset + limit);
  return { items: page, total: items.length };
}
