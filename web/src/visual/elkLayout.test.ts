import { afterEach, describe, expect, it, vi } from 'vitest';
import { type ElkHandle, LAYOUT_TIMEOUT_MS, withLayoutGuards } from './elkLayout';

const handle = (failed: Promise<never> = new Promise<never>(() => undefined)) => {
  const dispose = vi.fn();
  return { handle: { elk: {} as ElkHandle['elk'], failed, dispose } satisfies ElkHandle, dispose };
};

describe('withLayoutGuards', () => {
  afterEach(() => {
    vi.useRealTimers();
  });

  it('rejects a layout that never finishes and terminates its worker', async () => {
    vi.useFakeTimers();
    const { handle: h, dispose } = handle();
    const guarded = withLayoutGuards(h, new Promise<never>(() => undefined));
    const settled = expect(guarded).rejects.toThrow('the layout timed out');
    await vi.advanceTimersByTimeAsync(LAYOUT_TIMEOUT_MS - 1);
    expect(dispose).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    await settled;
    expect(dispose).toHaveBeenCalledOnce();
  });

  it('passes a finished layout through and leaves the worker running', async () => {
    vi.useFakeTimers();
    const { handle: h, dispose } = handle();
    await expect(withLayoutGuards(h, Promise.resolve('laid out'))).resolves.toBe('laid out');
    await vi.advanceTimersByTimeAsync(LAYOUT_TIMEOUT_MS * 2);
    expect(dispose).not.toHaveBeenCalled();
  });

  it('rejects as soon as the worker fails', async () => {
    const { handle: h } = handle(Promise.reject(new Error('the layout worker failed to load')));
    await expect(withLayoutGuards(h, new Promise<never>(() => undefined))).rejects.toThrow(
      'failed to load',
    );
  });
});
