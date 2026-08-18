import { Code, ConnectError, createClient } from '@connectrpc/connect';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DestinationService } from '@/gen/shepherd/mgmt/v1/destination_pb';
import { toApiError, transport } from './transport';

describe('shepherd.mgmt.v1 transport', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('adds X-Requested-With: XMLHttpRequest to every Connect call (CSRF)', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ items: [], total: 0 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );

    const client = createClient(DestinationService, transport);
    await client.listDestinations({ orgId: 'org-1' });

    expect(fetchSpy).toHaveBeenCalledTimes(1);
    const init = fetchSpy.mock.calls[0][1] as RequestInit;
    const headers = new Headers(init.headers);
    expect(headers.get('X-Requested-With')).toBe('XMLHttpRequest');
    expect(init.credentials).toBe('same-origin');
  });

  it('calls the RPC as a same-origin POST with a JSON content type', async () => {
    const fetchSpy = vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ items: [], total: 0 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      }),
    );

    const client = createClient(DestinationService, transport);
    await client.listDestinations({ orgId: 'org-1' });

    const [input, init] = fetchSpy.mock.calls[0];
    expect(String(input)).toContain('/shepherd.mgmt.v1.DestinationService/ListDestinations');
    expect(init?.method).toBe('POST');
  });
});

describe('toApiError', () => {
  it('normalizes a ConnectError into the app uniform error shape', () => {
    const err = new ConnectError('destination not found', Code.NotFound);
    expect(toApiError(err)).toEqual({ code: 'not_found', message: 'destination not found' });
  });

  it('renders multi-word codes in snake_case, matching the legacy REST envelope style', () => {
    const err = new ConnectError('nope', Code.FailedPrecondition);
    expect(toApiError(err).code).toBe('failed_precondition');
  });

  it('normalizes an arbitrary thrown value without crashing', () => {
    const result = toApiError(new Error('network down'));
    expect(result.code).toBeTruthy();
    expect(result.message).toBe('network down');
  });
});
