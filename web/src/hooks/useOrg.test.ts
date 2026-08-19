import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  getPersistedOrgId,
  ORG_STORAGE_KEY,
  type OrgSummary,
  resolveOrgId,
  setPersistedOrgId,
} from './useOrg';

// Regression coverage for B3 (docs/project-status.md): useOrgId() used to be
// hardcoded to me.orgs[0], permanently pinning every org-scoped page to the
// alphabetically-first org. These tests cover the two pieces of logic that
// replace it: the persisted-id <-> localStorage round trip, and the pure
// fallback rule that ignores a stale/foreign persisted id.

const orgA: OrgSummary = { id: 'org-a', name: 'a', displayName: 'Org A', role: 'admin' };
const orgB: OrgSummary = { id: 'org-b', name: 'b', displayName: 'Org B', role: 'admin' };

describe('resolveOrgId (pure fallback rule)', () => {
  it('keeps the persisted id when it names one of the orgs', () => {
    expect(resolveOrgId([orgA, orgB], 'org-b')).toBe('org-b');
  });

  it('falls back to the first org when the persisted id belongs to no org', () => {
    expect(resolveOrgId([orgA, orgB], 'org-deleted')).toBe('org-a');
  });

  it('falls back to the first org when nothing is persisted', () => {
    expect(resolveOrgId([orgA, orgB], '')).toBe('org-a');
  });

  it('returns empty when there are no orgs at all', () => {
    expect(resolveOrgId([], 'org-a')).toBe('');
  });
});

describe('getPersistedOrgId / setPersistedOrgId (localStorage round trip)', () => {
  // No jsdom in this project's vitest setup — install a minimal
  // window/localStorage stand-in so the module's `typeof window` guard
  // takes the browser branch, same as it does in the real app.
  class MemoryStorage {
    private store = new Map<string, string>();
    getItem(key: string) {
      return this.store.has(key) ? this.store.get(key)! : null;
    }
    setItem(key: string, value: string) {
      this.store.set(key, value);
    }
    removeItem(key: string) {
      this.store.delete(key);
    }
    clear() {
      this.store.clear();
    }
  }

  beforeEach(() => {
    (globalThis as unknown as { window: unknown }).window = {
      localStorage: new MemoryStorage(),
      // no-op: getPersistedOrgId/setPersistedOrgId don't subscribe, only useOrg() does
      addEventListener: () => undefined,
      removeEventListener: () => undefined,
    };
  });

  afterEach(() => {
    delete (globalThis as unknown as { window?: unknown }).window;
  });

  it('returns "" when nothing has been persisted yet', () => {
    expect(getPersistedOrgId()).toBe('');
  });

  it('round-trips a persisted org id', () => {
    setPersistedOrgId('org-b');
    expect(getPersistedOrgId()).toBe('org-b');
  });

  it('stores under the documented key', () => {
    setPersistedOrgId('org-a');
    const win = (globalThis as unknown as { window: { localStorage: MemoryStorage } }).window;
    expect(win.localStorage.getItem(ORG_STORAGE_KEY)).toBe('org-a');
  });

  it('is a no-op (never throws) when window is unavailable', () => {
    delete (globalThis as unknown as { window?: unknown }).window;
    expect(() => setPersistedOrgId('org-a')).not.toThrow();
    expect(getPersistedOrgId()).toBe('');
  });
});
