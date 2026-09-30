import { afterEach, describe, expect, it, vi } from 'vitest';
import { passwordChangeRequired } from './localAuth';

function authJSON(status: number, code: string): Response {
  return new Response(JSON.stringify({ error: { code, message: 'x' } }), {
    status,
    headers: { 'Content-Type': 'application/json' },
  });
}

describe('passwordChangeRequired', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('asks the Connect GetMe procedure, not the REST shim, as a CSRF-safe JSON POST', async () => {
    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(authJSON(403, 'password_change_required'));

    await passwordChangeRequired();

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const [input, init] = fetchSpy.mock.calls[0];
    expect(String(input)).toBe('/shepherd.mgmt.v1.MeService/GetMe');
    expect(init?.method).toBe('POST');
    expect(init?.credentials).toBe('same-origin');
    expect(init?.body).toBe('{}');
    const headers = new Headers(init?.headers);
    expect(headers.get('Content-Type')).toBe('application/json');
    expect(headers.get('X-Requested-With')).toBe('XMLHttpRequest');
  });

  it('is true for a 403 carrying password_change_required', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(authJSON(403, 'password_change_required'));
    await expect(passwordChangeRequired()).resolves.toBe(true);
  });

  it('is false for a 403 with any other code', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(authJSON(403, 'permission_denied'));
    await expect(passwordChangeRequired()).resolves.toBe(false);
  });

  it('is false for a 403 whose body is not JSON', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('nope', { status: 403 }));
    await expect(passwordChangeRequired()).resolves.toBe(false);
  });

  it('is false when the session is simply unauthenticated (401)', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ code: 'unauthenticated', message: 'not authenticated' }), {
        status: 401,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    await expect(passwordChangeRequired()).resolves.toBe(false);
  });

  it('is false when GetMe succeeds', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ userOid: 'u1' }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    await expect(passwordChangeRequired()).resolves.toBe(false);
  });

  it('is false when the request itself fails', async () => {
    vi.spyOn(globalThis, 'fetch').mockRejectedValue(new TypeError('network down'));
    await expect(passwordChangeRequired()).resolves.toBe(false);
  });
});
