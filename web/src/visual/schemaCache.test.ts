import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  cachedCurrentSchema,
  loadCurrentSchema,
  prefetchCurrentSchema,
  resetSchemaCacheForTests,
} from './schemaCache';

const payload = (v: string) => ({ _meta: { alloy_version: v }, components: {} });

function respond(body: unknown, etag: string | null) {
  const json = vi.fn(async () => body);
  const res = {
    ok: true,
    status: 200,
    headers: { get: (h: string) => (h.toLowerCase() === 'etag' ? etag : null) },
    json,
    body: { cancel: vi.fn(async () => undefined) },
  };
  return { res, json };
}

describe('schemaCache (#251)', () => {
  let fetchMock: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    resetSchemaCacheForTests();
    fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
  });
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('shares one request between the route prefetch and the builder', async () => {
    const { res } = respond(payload('v1.20.1'), '"a"');
    fetchMock.mockResolvedValue(res);
    const fromRoute = prefetchCurrentSchema();
    const fromBuilder = loadCurrentSchema();
    expect(await fromBuilder).toBe(await fromRoute);
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock.mock.calls[0]?.[0]).toBe('/api/schema/current');
  });

  it('answers a builder mount right after the prefetch from the held copy', async () => {
    fetchMock.mockResolvedValue(respond(payload('v1.20.1'), '"a"').res);
    const first = await prefetchCurrentSchema();
    expect(cachedCurrentSchema()).toBe(first);
    expect(await loadCurrentSchema()).toBe(first);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('revalidates a held copy that is no longer fresh, without re-parsing an unchanged one', async () => {
    vi.useFakeTimers();
    fetchMock.mockResolvedValueOnce(respond(payload('v1.20.1'), '"a"').res);
    const first = await prefetchCurrentSchema();

    vi.advanceTimersByTime(60_000);
    const unchanged = respond(payload('v1.20.1'), '"a"');
    fetchMock.mockResolvedValueOnce(unchanged.res);
    // Same ETag: the very same object comes back and the body is not parsed.
    expect(await loadCurrentSchema()).toBe(first);
    expect(unchanged.json).not.toHaveBeenCalled();
    expect(fetchMock).toHaveBeenCalledTimes(2);

    vi.advanceTimersByTime(60_000);
    fetchMock.mockResolvedValueOnce(respond(payload('v1.21.0'), '"b"').res);
    // A server upgrade moved `current`: the new payload replaces the held one.
    const next = await loadCurrentSchema();
    expect(next).not.toBe(first);
    expect(next).toEqual(payload('v1.21.0'));
    expect(cachedCurrentSchema()).toBe(next);
  });

  it('a failed fetch is not cached: the next call tries again', async () => {
    fetchMock.mockResolvedValueOnce({ ok: false, status: 503, headers: { get: () => null } });
    await expect(prefetchCurrentSchema()).rejects.toThrow(/503/);
    expect(cachedCurrentSchema()).toBeNull();
    fetchMock.mockResolvedValueOnce(respond(payload('v1.20.1'), null).res);
    expect(await loadCurrentSchema()).toEqual(payload('v1.20.1'));
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });
});
