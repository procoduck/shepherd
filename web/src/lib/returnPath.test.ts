import { describe, expect, it } from 'vitest';
import { loginHref, oidcLoginHref, safeReturnPath } from './returnPath';

// Mirrors internal/auth/returnpath_test.go's table: the two validators guard
// the two sign-in legs and must agree.
describe('safeReturnPath', () => {
  it.each([
    '/pipelines',
    '/pipelines/0b6f/visual',
    '/audit?actor=alice&page=2',
    '/collectors/abc#labels',
    '/pipelines/a%20b',
    '/',
  ])('accepts %s', (raw) => {
    expect(safeReturnPath(raw)).toBe(raw);
  });

  it.each([
    '',
    'https://evil.example/',
    'http://evil.example',
    'javascript:alert(1)',
    'data:text/html,<script>alert(1)</script>',
    'https:evil.example',
    'pipelines',
    '//evil.example',
    '//evil.example/pipelines',
    '///evil.example',
    '/\\evil.example',
    '\\\\evil.example',
    '/pipelines\\..\\\\evil.example',
    '/\t/evil.example',
    '/\n/evil.example',
    '/pipelines\r\nSet-Cookie: x=y',
    '/pipelines\u0000',
    '/%2Fevil.example',
    '/%2fevil.example',
    '%2F%2Fevil.example',
    '/%5Cevil.example',
    '/%5cevil.example',
    '/%252Fevil.example',
    '/%255Cevil.example',
    '/%09/evil.example',
    '/%0A/evil.example',
    '/pipelines%zz',
    '//user@evil.example',
    '/login',
    '/login?next=/pipelines',
    '/auth/logout',
    `/${'a'.repeat(4096)}`,
  ])('refuses %j', (raw) => {
    expect(safeReturnPath(raw)).toBeNull();
  });

  it('refuses null and undefined', () => {
    expect(safeReturnPath(null)).toBeNull();
    expect(safeReturnPath(undefined)).toBeNull();
  });
});

describe('loginHref / oidcLoginHref', () => {
  it('carries a safe path, encoded', () => {
    expect(loginHref('/pipelines/p1?tab=graph')).toBe(
      '/login?next=%2Fpipelines%2Fp1%3Ftab%3Dgraph',
    );
    expect(oidcLoginHref('/pipelines/p1')).toBe('/auth/login?next=%2Fpipelines%2Fp1');
  });

  it('drops "/" and anything unsafe', () => {
    expect(loginHref('/')).toBe('/login');
    expect(loginHref('//evil.example')).toBe('/login');
    expect(oidcLoginHref(null)).toBe('/auth/login');
    expect(oidcLoginHref('/')).toBe('/auth/login');
  });
});
