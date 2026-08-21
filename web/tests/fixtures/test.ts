import { test as base, expect, type Route } from '@playwright/test';
import { defaultState, installDefaultHandlers } from '../mocks/handlers';
import { type Handler, type MockState, Router } from '../mocks/router';
import type { MeResponse } from './personas';

interface ApiFixture {
  state: MockState;
  seed: (partial: Partial<MockState>) => void;
  loginAs: (persona: MeResponse) => Promise<void>;
  override: (method: string, path: string, handler: Handler) => void;
  failNext: (method: string, path: string, status?: number, error?: string) => void;
  delay: (method: string, path: string, ms: number) => void;
  calls: (pattern: string) => Array<{ method: string; path: string; body: unknown }>;
  idle: () => Promise<void>;
}

export const test = base.extend<{ api: ApiFixture }>({
  api: async ({ page }, use) => {
    const state = defaultState();
    const router = new Router(state);
    installDefaultHandlers(router);

    const recorded: Array<{ method: string; path: string; body: unknown }> = [];

    // Every (status, pathname) pair a mock handler deliberately fulfilled
    // with an error. The browser logs "Failed to load resource" for each of
    // these; the console-error check below allows exactly this set, so an
    // intentional injected failure (failNext, an error-returning override,
    // or seeded error state) passes while any other failed load — including
    // one on a DIFFERENT endpoint that happens to share the status — still
    // fails the test. 599 (unmatched mock) is deliberately never tracked.
    const errorKey = (status: number, pathname: string) => `${status} ${pathname}`;
    const injectedErrors = new Set<string>();
    const trackErrors = (route: Route) =>
      new Proxy(route, {
        get(target, prop) {
          if (prop === 'fulfill') {
            return (opts?: Parameters<typeof target.fulfill>[0]) => {
              const status = opts?.status ?? 200;
              if (status >= 400 && status !== 599) {
                injectedErrors.add(errorKey(status, new URL(target.request().url()).pathname));
              }
              return target.fulfill(opts);
            };
          }
          const value = Reflect.get(target, prop, target);
          return typeof value === 'function' ? value.bind(target) : value;
        },
      });

    // Install interception. Matches the surviving REST surface (/api/*,
    // /auth/*) plus every shepherd.mgmt.v1 Connect procedure POST
    // (/shepherd.mgmt.v1.<Service>/<Method>).
    await page.route(
      (url) => /^\/(api|auth)\//.test(url.pathname) || /^\/shepherd\.mgmt\.v1\./.test(url.pathname),
      async (route) => {
        const req = route.request();
        const url = new URL(req.url());
        let body: unknown = null;
        try {
          body = req.postDataJSON();
        } catch {
          /* not JSON */
        }
        recorded.push({ method: req.method(), path: url.pathname, body });
        await router.handle(trackErrors(route));
      },
    );

    // Catch console errors
    const consoleErrors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        const text = msg.text();
        // Allowlist expected errors:
        // - 401 from /api/me is expected when testing unauthenticated state
        // - Failed resource loads whose status AND URL a mock handler
        //   intentionally fulfilled (see injectedErrors above). The message
        //   text carries only the status; in Chromium the failing resource's
        //   URL rides in msg.location().url.
        if (text.includes('401') || text.includes('Unauthorized')) return;
        if (text.includes('Failed to load resource')) {
          const status = /status of (\d{3})/.exec(text);
          const resourceUrl = msg.location().url;
          if (status && resourceUrl) {
            try {
              if (injectedErrors.has(errorKey(Number(status[1]), new URL(resourceUrl).pathname))) {
                return;
              }
            } catch {
              /* unparseable URL — fall through and record the error */
            }
          }
        }
        consoleErrors.push(text);
      }
    });

    const fixture: ApiFixture = {
      state,

      seed(partial) {
        Object.assign(state, partial);
      },

      async loginAs(persona) {
        state.me = persona;
        // Register an init script that sets window.__initialMe before React mounts.
        // addInitScript runs before every subsequent page.goto, so org-scoped queries
        // in useMe() initialData can fire synchronously on first render.
        await page.addInitScript((me) => {
          (window as unknown as Record<string, unknown>).__initialMe = me;
        }, persona as unknown);
      },

      override(method, path, handler) {
        router.register(method, path, handler);
      },

      failNext(method, path, status = 500, error = 'internal_error') {
        let remaining = 2; // fail initial request + one retry so isError stays true
        // shepherd.mgmt.v1.* paths are Connect RPCs: the client parses a
        // non-200 body as {code, message} (see @connectrpc/connect's
        // protocol-connect/error-json.ts) rather than the legacy REST
        // {error:{code,message}} envelope.
        const isRpc = path.startsWith('/shepherd.mgmt.v1.');
        // Once exhausted, delegate to the handlers registered before this one
        // — route.continue() would hit the live vite preview, which has no
        // backend and 404s everything in this suite.
        const prior = router.snapshot();
        router.register(method, path, (route) => {
          if (remaining > 0) {
            remaining--;
            return route.fulfill({
              status,
              contentType: 'application/json',
              body: JSON.stringify(
                isRpc
                  ? { code: error, message: `Simulated ${status}` }
                  : { error: { code: error, message: `Simulated ${status}` } },
              ),
            });
          }
          return router.handle(route, prior);
        });
      },

      delay(method, path, ms) {
        // Delay, then serve what the previously-registered (or default)
        // handler would have — not route.continue() into the backend-less
        // preview server.
        const prior = router.snapshot();
        router.register(method, path, async (route) => {
          await new Promise((r) => setTimeout(r, ms));
          await router.handle(route, prior);
        });
      },

      calls(pattern) {
        return recorded.filter((c) => c.path.includes(pattern));
      },

      async idle() {
        // Wait until no in-flight requests for 100ms
        await page.waitForLoadState('networkidle').catch(() => {
          /* ignore timeout */
        });
      },
    };

    await use(fixture);

    // AfterEach assertions
    if (state.unmatched.length > 0) {
      throw new Error(`Unmatched API requests:\n${state.unmatched.join('\n')}`);
    }
    if (consoleErrors.length > 0) {
      throw new Error(`Console errors:\n${consoleErrors.join('\n')}`);
    }
  },
});

export { expect };
