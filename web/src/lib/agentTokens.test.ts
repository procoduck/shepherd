import { describe, expect, it } from 'vitest';
import {
  bindableOrgs,
  isUnbound,
  orgLabel,
  sortTokens,
  splitAllowlist,
  tokenScope,
} from './agentTokens';

const a = { id: 'org-a', name: 'org-a', displayName: 'Org A' };
const b = { id: 'org-b', name: 'org-b', displayName: 'org-b' };

describe('agent tokens', () => {
  it('scopes an org admin to the org in view, and an app admin to every token', () => {
    expect(tokenScope(false, 'org-a')).toBe('org-a');
    expect(tokenScope(true, 'org-a')).toBe('');
  });

  it('treats a token with no org as unbound', () => {
    expect(isUnbound({ orgId: '' })).toBe(true);
    expect(isUnbound({ orgId: 'org-a' })).toBe(false);
  });

  it('lists live unbound tokens first and otherwise keeps the order', () => {
    const items = [
      { name: 'bound-1', orgId: 'org-a', status: 'active' },
      { name: 'revoked-unbound', orgId: '', status: 'revoked' },
      { name: 'legacy', orgId: '', status: 'active' },
      { name: 'bound-2', orgId: 'org-b', status: 'active' },
    ];
    expect(sortTokens(items).map((t) => t.name)).toEqual([
      'legacy',
      'bound-1',
      'revoked-unbound',
      'bound-2',
    ]);
  });

  it('splits an allowlist on commas and whitespace, dropping blanks', () => {
    expect(splitAllowlist(' prod-eu-1, staging-eu-1  logs,,')).toEqual([
      'prod-eu-1',
      'staging-eu-1',
      'logs',
    ]);
    expect(splitAllowlist('')).toEqual([]);
  });

  it('offers an org admin only their own org, and an app admin every org once', () => {
    expect(bindableOrgs(false, 'org-a', [a, b], [])).toEqual([a]);
    expect(bindableOrgs(false, 'org-x', [a], [a, b])).toEqual([]);
    expect(bindableOrgs(true, 'org-a', [a], [a, b])).toEqual([a, b]);
    expect(bindableOrgs(true, 'org-a', [a], [])).toEqual([a]);
  });

  it('labels an org by display name and slug', () => {
    expect(orgLabel(a)).toBe('Org A (org-a)');
    expect(orgLabel(b)).toBe('org-b');
  });
});
