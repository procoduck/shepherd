// The local-user auth endpoints that live outside the Connect API
// (internal/auth/password_change.go, internal/auth/auth.go). They speak the
// auth handler's own JSON: `{"error":{"code","message"}}` on failure, which
// connect-web cannot decode — so a 403 from the password-change middleware
// reaches a Connect client as a bare PermissionDenied with no message. These
// helpers read that shape directly.

import { MeService } from '@/gen/shepherd/mgmt/v1/me_pb';

export const CHANGE_PASSWORD_PATH = '/change-password';

export interface AuthErrorBody {
  error?: { code?: string; message?: string };
}

export class LocalAuthError extends Error {
  readonly code: string;
  constructor(code: string, message: string) {
    super(message);
    this.code = code;
  }
}

async function authErrorFrom(response: Response): Promise<LocalAuthError> {
  let body: AuthErrorBody | undefined;
  try {
    body = (await response.json()) as AuthErrorBody;
  } catch {
    body = undefined;
  }
  return new LocalAuthError(
    body?.error?.code ?? `http_${response.status}`,
    body?.error?.message ?? `request failed (${response.status})`,
  );
}

/** POST /api/auth/local/password for the signed-in local user. */
export async function changePassword(currentPassword: string, newPassword: string): Promise<void> {
  const response = await fetch('/api/auth/local/password', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', 'X-Requested-With': 'XMLHttpRequest' },
    body: JSON.stringify({ current_password: currentPassword, new_password: newPassword }),
  });
  if (!response.ok) throw await authErrorFrom(response);
}

/**
 * The GetMe procedure's Connect URL, derived from the generated descriptor so
 * a rename in me.proto breaks the build rather than this probe.
 */
const GET_ME_PATH = `/${MeService.typeName}/${MeService.method.getMe.name}`;

/**
 * Whether the session's every request is being refused with
 * password_change_required. Asked only after a Connect call has already
 * failed.
 *
 * This is a plain fetch of the GetMe procedure rather than a call through
 * `clients.me`, on purpose: the middleware answers every route, Connect ones
 * included, with the auth JSON (`{"error":{"code":...}}`), which connect-web
 * cannot decode — its client surfaces only a bare PermissionDenied with an
 * empty message. Reading the 403 body directly recovers the code. The request
 * is the Connect unary JSON call the client would have sent: POST, an empty
 * GetMeRequest, and the X-Requested-With header the CSRF middleware demands.
 */
export async function passwordChangeRequired(): Promise<boolean> {
  try {
    const response = await fetch(GET_ME_PATH, {
      method: 'POST',
      credentials: 'same-origin',
      headers: {
        'Content-Type': 'application/json',
        'Connect-Protocol-Version': '1',
        'X-Requested-With': 'XMLHttpRequest',
      },
      body: '{}',
    });
    if (response.status !== 403) return false;
    const body = (await response.json()) as AuthErrorBody;
    return body?.error?.code === 'password_change_required';
  } catch {
    return false;
  }
}

/** Send the browser to the change-password screen unless it is already there. */
export function redirectToChangePassword(): void {
  if (window.location.pathname === CHANGE_PASSWORD_PATH) return;
  window.location.assign(`${CHANGE_PASSWORD_PATH}?required=1`);
}
