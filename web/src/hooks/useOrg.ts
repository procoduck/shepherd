import { useQueryClient } from '@tanstack/react-query';
import { useCallback, useEffect, useSyncExternalStore } from 'react';
import { useMe } from './useMe';

export interface OrgSummary {
  id: string;
  name: string;
  displayName: string;
  role: string;
}

export const ORG_STORAGE_KEY = 'shepherd.orgId';

// A tiny external store for the persisted org selection. Kept outside React
// state so every consumer (Shell's switcher, every org-scoped page) reads
// the same value without needing a context provider wrapped around the
// whole app.
const listeners = new Set<() => void>();

/** Reads the persisted org id, or '' if unset/unavailable. Exported for tests. */
export function getPersistedOrgId(): string {
  if (typeof window === 'undefined') return '';
  try {
    return window.localStorage.getItem(ORG_STORAGE_KEY) ?? '';
  } catch {
    // localStorage can throw (private browsing, quota, disabled) — treat as unset.
    return '';
  }
}

/** Persists the org id and notifies subscribers. Exported for tests. */
export function setPersistedOrgId(id: string): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(ORG_STORAGE_KEY, id);
  } catch {
    /* ignore persistence failures */
  }
  for (const listener of listeners) listener();
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  const onStorage = (e: StorageEvent) => {
    if (e.key === ORG_STORAGE_KEY) listener();
  };
  if (typeof window !== 'undefined') window.addEventListener('storage', onStorage);
  return () => {
    listeners.delete(listener);
    if (typeof window !== 'undefined') window.removeEventListener('storage', onStorage);
  };
}

function getServerSnapshot(): string {
  return '';
}

/**
 * Pure fallback rule: the persisted org id wins if it names one of the
 * user's orgs, otherwise the first org, otherwise '' (no orgs / not loaded).
 * Exported standalone so the fallback logic is testable without a DOM or a
 * React render.
 */
export function resolveOrgId(orgs: OrgSummary[], persisted: string): string {
  return orgs.some((o) => o.id === persisted) ? persisted : (orgs[0]?.id ?? '');
}

/**
 * Org selection: persisted (localStorage) and validated against the orgs
 * returned by /api/me. Falls back to the first org when the persisted id
 * belongs to no org the user has (deleted org, stale browser state, or
 * simply never set).
 */
export function useOrg(): { orgId: string; orgs: OrgSummary[]; setOrgId: (id: string) => void } {
  const { data: me } = useMe();
  const queryClient = useQueryClient();
  const raw = useSyncExternalStore(subscribe, getPersistedOrgId, getServerSnapshot);
  const orgs = (me?.orgs ?? []) as OrgSummary[];

  const orgId = resolveOrgId(orgs, raw);

  // Self-heal persisted storage: if the stored id was empty/stale and we
  // fell back to another org, persist that so subsequent loads land on the
  // same org without re-running the fallback logic every time.
  useEffect(() => {
    if (orgId && orgId !== raw) setPersistedOrgId(orgId);
  }, [orgId, raw]);

  const setOrgId = useCallback(
    (id: string) => {
      if (!orgs.some((o) => o.id === id) || id === orgId) return;
      setPersistedOrgId(id);
      // Every org-scoped query key carries orgId, so switching already
      // points pages at fresh cache entries — this invalidation guards
      // against any query that (by omission) doesn't, and forces an
      // immediate refetch instead of silently serving stale cache.
      queryClient.invalidateQueries({ predicate: (q) => q.queryKey[0] !== 'me' });
    },
    [orgs, orgId, queryClient],
  );

  return { orgId, orgs, setOrgId };
}

/** Returns the ID of the selected org, or '' if not loaded/no orgs. */
export function useOrgId(): string {
  return useOrg().orgId;
}
