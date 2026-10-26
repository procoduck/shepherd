import { ConnectError } from '@connectrpc/connect';
import { describe, expect, it } from 'vitest';
import { savedToastText, strictRefusalNote } from './OrgStrictTenantsToggle';

// 0.20 walkthrough: a refused strict toggle showed its blockers inline, then
// Save closed the dialog with "Organisation updated" while strict stayed off.
// The note the toggle reports up is what the Save toast says instead.
describe('strictRefusalNote', () => {
  it('counts the blockers when the preflight listed them', () => {
    expect(strictRefusalNote(true, 1, undefined)).toBe(
      'strict tenant mode was not enabled: 1 item uses a tenant this organisation does not own',
    );
    expect(strictRefusalNote(true, 3, undefined)).toBe(
      'strict tenant mode was not enabled: 3 items use a tenant this organisation does not own',
    );
  });

  it("falls back to the refusal's own message", () => {
    expect(strictRefusalNote(false, 0, new ConnectError('database unavailable'))).toBe(
      'strict tenant mode was not disabled: Database unavailable',
    );
  });
});

describe('savedToastText', () => {
  it('is the plain success without a refusal', () => {
    expect(savedToastText(null)).toBe('Organisation updated');
  });

  it('says strict mode is not on when the last toggle attempt was refused', () => {
    expect(savedToastText('strict tenant mode was not enabled: x')).toBe(
      'Organisation updated — strict tenant mode was not enabled: x',
    );
  });
});
