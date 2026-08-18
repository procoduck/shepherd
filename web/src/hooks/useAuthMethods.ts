import { useQuery } from '@tanstack/react-query';

export interface AuthMethods {
  oidc: boolean;
  local_admin: boolean;
}

async function fetchAuthMethods(): Promise<AuthMethods> {
  const resp = await fetch('/auth/methods');
  if (!resp.ok) return { oidc: false, local_admin: false };
  return resp.json() as Promise<AuthMethods>;
}

export function useAuthMethods() {
  return useQuery<AuthMethods>({
    queryKey: ['auth-methods'],
    queryFn: fetchAuthMethods,
    staleTime: 60_000,
    retry: false,
  });
}
