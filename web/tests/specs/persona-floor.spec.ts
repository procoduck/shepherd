/**
 * Actor skew guard (W7-08).
 *
 * Reads every tests/specs/*.spec.ts source file with node:fs at module load
 * (the same static-source-scan approach fullstack/protected-routes.spec.ts
 * uses for router.tsx) and counts literal `loginAs(<persona>)` call sites
 * per persona. This is a source scan, not a runtime trace, so it also
 * counts calls inside a `test.describe.skip` block (route-guard.spec.ts) —
 * deliberately: that file's matrix is real, reviewed coverage that simply
 * cannot execute until W6-S7 lands, and the alternative (deleting it, or
 * writing it with non-literal persona references) would either lose the
 * coverage or make it invisible to this guard.
 *
 * appAdmin dominated the suite (86/108 personas at the time this guard was
 * written, ~80%) because it is the path of least resistance: every spec can
 * reach for it and be sure nothing is denied. The floors below force real,
 * additional coverage of the roles the product actually ships — org admin,
 * org editor, viewer, local admin and the zero-org case — rather than
 * papering over the ratio by relabeling existing appAdmin tests.
 */
import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { expect, test } from '@playwright/test';

const SPECS_DIR = path.dirname(fileURLToPath(import.meta.url));

const PERSONAS = ['appAdmin', 'orgAdmin', 'orgEditor', 'viewer', 'localAdmin', 'nobody'] as const;
type Persona = (typeof PERSONAS)[number];

const FLOORS: Partial<Record<Persona, number>> = {
  orgAdmin: 16,
  orgEditor: 10,
  viewer: 10,
  localAdmin: 3,
  nobody: 4,
};
const APP_ADMIN_MAX_SHARE = 0.6;

function specFiles(): string[] {
  return readdirSync(SPECS_DIR).filter((f) => f.endsWith('.spec.ts'));
}

function countLoginAsCalls(): Record<Persona, number> {
  const counts = Object.fromEntries(PERSONAS.map((p) => [p, 0])) as Record<Persona, number>;
  for (const file of specFiles()) {
    const source = readFileSync(path.join(SPECS_DIR, file), 'utf8');
    for (const persona of PERSONAS) {
      const matches = source.match(new RegExp(`loginAs\\(\\s*${persona}\\s*\\)`, 'g'));
      counts[persona] += matches?.length ?? 0;
    }
  }
  return counts;
}

test('every persona clears its RBAC-coverage floor', () => {
  const counts = countLoginAsCalls();
  const total = Object.values(counts).reduce((a, b) => a + b, 0);

  for (const persona of Object.keys(FLOORS) as Persona[]) {
    expect(
      counts[persona],
      `${persona} loginAs() call sites across tests/specs/*.spec.ts`,
    ).toBeGreaterThanOrEqual(FLOORS[persona] as number);
  }

  expect(
    counts.appAdmin,
    `appAdmin share of loginAs() call sites (${counts.appAdmin}/${total})`,
  ).toBeLessThanOrEqual(APP_ADMIN_MAX_SHARE * total);
});
