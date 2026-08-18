import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import type { GetMeResponse } from '@/gen/shepherd/mgmt/v1/me_pb';

export type MeData = GetMeResponse;

async function fetchMe(): Promise<MeData | null> {
  // Mirrors the legacy fetcher: any failure (401/403 unauthenticated, or any
  // other transport error) resolves to "not signed in" rather than an error
  // state, matching the app's login-redirect behaviour.
  try {
    return await clients.me.getMe({});
  } catch {
    return null;
  }
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
