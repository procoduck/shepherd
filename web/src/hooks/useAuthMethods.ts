import { useQuery } from '@tanstack/react-query';

export interface AuthMethods {
  /** True only when a discovered provider is live — i.e. sign-in actually works. */
  oidc: boolean;
  local_admin: boolean;
  /** Login button label for the active provider ("Microsoft", "Okta", ...). */
  oidc_display_name?: string;
  /** Preset key of the active provider ("entra", "okta", ...). */
  oidc_provider?: string;
}

const NO_METHODS: AuthMethods = { oidc: false, local_admin: false };

async function fetchAuthMethods(): Promise<AuthMethods> {
  const resp = await fetch('/auth/methods');
  if (!resp.ok) return NO_METHODS;
  return resp.json() as Promise<AuthMethods>;
}

export function useAuthMethods() {
  return useQuery<AuthMethods>({
    queryKey: ['auth-methods'],
    queryFn: fetchAuthMethods,
    // Not cached the way it used to be (60s): an app admin can now turn OIDC
    // on from the settings page, and the next thing they do is reload the
    // login page to check it worked.
    staleTime: 0,
    retry: false,
  });
}
