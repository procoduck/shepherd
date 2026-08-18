import { useQuery } from '@tanstack/react-query';

export interface MeData {
  user_oid: string;
  email: string;
  display_name: string;
  is_app_admin: boolean;
  auth_method: string;
  orgs: Array<{ id: string; name: string; display_name: string; role: string }>;
}

async function fetchMe(): Promise<MeData | null> {
  const resp = await fetch('/api/me');
  if (resp.status === 401 || resp.status === 403) return null;
  if (!resp.ok) return null;
  return resp.json() as Promise<MeData>;
}

export function useMe() {
  return useQuery<MeData | null>({
    queryKey: ['me'],
    queryFn: fetchMe,
    staleTime: 5 * 60_000,
    retry: false,
    // In mocked test environments, window.__initialMe may be pre-populated
    // to avoid an async round-trip before org-scoped queries can fire.
    initialData: () => {
      if (typeof window !== 'undefined') {
        const pre = (window as unknown as Record<string, unknown>).__initialMe;
        if (pre) return pre as MeData;
      }
      return undefined;
    },
    initialDataUpdatedAt: () => {
      if (
        typeof window !== 'undefined' &&
        (window as unknown as Record<string, unknown>).__initialMe
      ) {
        return Date.now(); // treat as fresh so it doesn't immediately refetch
      }
      return 0;
    },
  });
}
