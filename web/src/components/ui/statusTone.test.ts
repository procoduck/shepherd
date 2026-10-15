import { describe, expect, it } from 'vitest';
import { syncStatusTone } from './statusTone';

describe('syncStatusTone', () => {
  it.each([
    ['ok', 'ok'],
    ['error', 'danger'],
    ['warning', 'warn'],
    ['', 'neutral'],
    // A status this build does not know must not read as a success.
    ['something-new', 'neutral'],
  ])('maps %j to %s', (status, tone) => {
    expect(syncStatusTone(status)).toBe(tone);
  });
});
