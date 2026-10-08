// Carrying the page a signed-out user asked for through the sign-in round
// trip (#250). The value travels as /login?next=<path>; whatever comes back
// out is navigated to, so it must be a same-origin relative path and nothing
// else — an open redirect on the sign-in page is a phishing primitive.
//
// This is the client-side twin of internal/auth/returnpath.go's
// SafeReturnPath, which guards the OIDC leg (the server stores and re-reads
// the path itself). Local sign-in never sends the path to the server, so this
// copy is the only check on that leg. Keep the two in step.

const MAX_LEN = 1024;
const MAX_DECODES = 3;

function shapeOk(p: string): boolean {
  if (!p.startsWith('/') || p.startsWith('//')) return false;
  for (let i = 0; i < p.length; i++) {
    const c = p.charCodeAt(i);
    // Browsers treat "\" as "/" and strip tabs/newlines before resolving, so
    // "/\evil" and "/\t/evil" both become the protocol-relative "//evil".
    if (c === 0x5c || c < 0x20 || c === 0x7f) return false;
  }
  return true;
}

/**
 * Returns `raw` when it is a safe same-origin relative path to return to
 * after sign-in, otherwise null. Refuses absolute and protocol-relative URLs,
 * backslashes and control characters (at every percent-decoded layer), and
 * /login or /auth/* (a loop rather than a return).
 */
export function safeReturnPath(raw: string | null | undefined): string | null {
  if (!raw || raw.length > MAX_LEN) return null;
  let candidate = raw;
  for (let i = 0; ; i++) {
    if (!shapeOk(candidate)) return null;
    if (!candidate.includes('%')) break;
    if (i === MAX_DECODES) return null;
    try {
      candidate = decodeURIComponent(candidate);
    } catch {
      return null;
    }
  }
  let url: URL;
  try {
    // Resolving against a throwaway origin and checking it survived is the
    // last line: anything that escapes the origin changes it.
    url = new URL(raw, 'http://shepherd.invalid');
  } catch {
    return null;
  }
  if (url.origin !== 'http://shepherd.invalid') return null;
  const path = url.pathname;
  if (path === '/login' || path.startsWith('/login/')) return null;
  if (path === '/auth' || path.startsWith('/auth/')) return null;
  return raw;
}

/** The current location as a return path: pathname + search + hash. */
export function currentReturnPath(): string {
  const { pathname, search, hash } = window.location;
  return `${pathname}${search}${hash}`;
}

/** The /login URL that will bring the user back to `path` once signed in.
 *  A path that is "/" or unsafe yields a bare /login. */
export function loginHref(path: string): string {
  const safe = safeReturnPath(path);
  return safe && safe !== '/' ? `/login?next=${encodeURIComponent(safe)}` : '/login';
}

/** The OIDC sign-in URL, forwarding a safe return path to the server, which
 *  validates it again (SafeReturnPath) before remembering it. */
export function oidcLoginHref(next: string | null): string {
  return next && next !== '/' ? `/auth/login?next=${encodeURIComponent(next)}` : '/auth/login';
}

/** The validated ?next= of the current location, or null. */
export function nextFromLocation(): string | null {
  return safeReturnPath(new URLSearchParams(window.location.search).get('next'));
}
