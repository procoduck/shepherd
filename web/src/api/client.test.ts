import { afterEach, describe, expect, it, vi } from 'vitest';
import { orgApi } from './client';

describe('API client CSRF header', () => {
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('sends X-Requested-With: XMLHttpRequest on mutation requests', async () => {
    const fetchSpy = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValue(new Response('{}', { status: 200 }));

    await orgApi.createDestination('test', { name: 'test' });

    expect(fetchSpy).toHaveBeenCalledWith(
      '/api/orgs/test/destinations',
      expect.objectContaining({
        method: 'POST',
        headers: expect.objectContaining({
          'X-Requested-With': 'XMLHttpRequest',
        }),
      }),
    );
  });
});
