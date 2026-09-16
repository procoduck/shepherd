// `make dev-frontend` runs the SPA under Vite's dev server against the compose
// backend on :8080, so every request the SPA makes must be forwarded by the
// proxy in vite.config.ts. The legacy REST shim lives under /api and the auth
// flow under /auth, but the primary API is Connect, posted to
// /shepherd.mgmt.v1.<Service>/<Method> on the SPA's own origin (transport.ts
// uses baseUrl '/'). Until 2026-09-15 only /api and /auth were proxied, so
// login worked and every list and detail screen then failed. This test pins
// the proxy to what the transport actually sends.
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { DestinationService } from '@/gen/shepherd/mgmt/v1/destination_pb';

// vite.config.ts is read as text rather than imported: it sits outside the
// typechecked src tree and carries vitest's `test` block, which vite's own
// config type does not know. The proxy table is a plain object literal, so
// its keys are recoverable with one regex.
function proxyKeys(): string[] {
  const source = readFileSync(new URL('../../vite.config.ts', import.meta.url), 'utf8');
  const block = /proxy:\s*\{([^}]*)\}/.exec(source);
  if (!block) throw new Error('no server.proxy block in vite.config.ts');
  return [...block[1].matchAll(/'((?:[^'\\]|\\.)*)'\s*:/g)].map((m) => m[1].replace(/\\\\/g, '\\'));
}

// Vite's rule: a key starting with ^ is a RegExp, anything else is a prefix.
function proxyMatches(keys: string[], pathname: string): boolean {
  return keys.some((key) =>
    key.startsWith('^') ? new RegExp(key).test(pathname) : pathname.startsWith(key),
  );
}

describe('vite dev-server proxy (make dev-frontend)', () => {
  const table = proxyKeys();

  it('forwards the Connect procedure paths the transport posts to', () => {
    const procedure = `/${DestinationService.typeName}/${DestinationService.method.listDestinations.name}`;
    expect(procedure).toMatch(/^\/shepherd\.mgmt\.v1\./);
    expect(proxyMatches(table, procedure)).toBe(true);
  });

  it('still forwards the REST shim and the auth flow', () => {
    expect(proxyMatches(table, '/api/me')).toBe(true);
    expect(proxyMatches(table, '/auth/callback')).toBe(true);
  });

  it('does not swallow SPA routes', () => {
    expect(proxyMatches(table, '/pipelines')).toBe(false);
    expect(proxyMatches(table, '/admin/users')).toBe(false);
  });
});

describe('build-info plugin', () => {
  // Under Vite 8 closeBundle also fires when the DEV server shuts down, so
  // without `apply: 'build'` every `make dev-frontend` session rewrote the
  // tracked internal/spa/dist/BUILD_INFO.json on exit (seen 2026-09-15).
  it('runs only for builds, so the dev server never touches the tracked dist', () => {
    const source = readFileSync(new URL('../../vite.config.ts', import.meta.url), 'utf8');
    const plugin = /const buildInfoPlugin\s*=\s*\{([\s\S]*?)\n\};/.exec(source);
    if (!plugin) throw new Error('no buildInfoPlugin in vite.config.ts');
    expect(plugin[1]).toMatch(/apply:\s*'build'/);
  });

  it('never inherits git stderr — the Docker web stage has no git', () => {
    // deploy/Dockerfile.local builds the SPA with no git in the context, so a
    // bare execSync('git ...') printed "/bin/sh: 1: git: not found" into every
    // image build. Every git call must silence the child's stderr.
    const source = readFileSync(new URL('../../vite.config.ts', import.meta.url), 'utf8');
    const plugin = /const buildInfoPlugin\s*=\s*\{([\s\S]*?)\n\};/.exec(source);
    if (!plugin) throw new Error('no buildInfoPlugin in vite.config.ts');
    const gitCalls = plugin[1].match(/execSync\([^)]*\)/g) ?? [];
    expect(gitCalls.length).toBeGreaterThan(0);
    for (const call of gitCalls) {
      expect(call).toMatch(/stdio:\s*\[\s*'ignore'\s*,\s*'pipe'\s*,\s*'ignore'\s*\]/);
    }
  });

  it('lets a caller inject the sha through the environment', () => {
    const source = readFileSync(new URL('../../vite.config.ts', import.meta.url), 'utf8');
    expect(source).toMatch(/process\.env\.SHEPHERD_BUILD_SHA/);
    expect(source).toMatch(/process\.env\.SHEPHERD_BUILD_DIRTY/);
  });
});
