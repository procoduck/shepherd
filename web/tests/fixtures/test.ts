import { test as base, expect } from '@playwright/test';
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

    // Install interception
    await page.route('**/{api,auth}/**', async (route) => {
      const req = route.request();
      const url = new URL(req.url());
      let body: unknown = null;
      try {
        body = req.postDataJSON();
      } catch {
        /* not JSON */
      }
      recorded.push({ method: req.method(), path: url.pathname, body });
      await router.handle(route);
    });

    // Catch console errors
    const consoleErrors: string[] = [];
    page.on('console', (msg) => {
      if (msg.type() === 'error') {
        const text = msg.text();
        // Allowlist expected errors:
        // - 401 from /api/me is expected when testing unauthenticated state
        // - React error #310 can fire transiently on first render during route setup
        // - Failed resource loads from mock error-injection tests (500/503/422) — these
        //   are browser-generated messages for intentional mock failure scenarios.
        if (text.includes('401') || text.includes('Unauthorized')) return;
        if (text.includes('React error #310') || text.includes('reactjs.org/docs/error-decoder'))
          return;
        if (
          text.includes('Failed to load resource') &&
          (text.includes('500') || text.includes('503') || text.includes('422'))
        )
          return;
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
        router.register(method, path, (route) => {
          if (remaining > 0) {
            remaining--;
            return route.fulfill({
              status,
              contentType: 'application/json',
              body: JSON.stringify({ error: { code: error, message: `Simulated ${status}` } }),
            });
          }
          return route.continue();
        });
      },

      delay(method, path, ms) {
        router.register(method, path, async (route) => {
          await new Promise((r) => setTimeout(r, ms));
          await route.continue();
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
