/**
 * #253: every wizard card used to carry the same AppWindow icon, so the catalog
 * gave no visual cue which card was which. Each registered kind now has its own
 * lucide icon, and a kind the SPA does not know falls back to a generic wand
 * rather than borrowing another wizard's icon.
 */
import { basicScenario } from '../fixtures/factories';
import { orgAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';
import { json } from '../mocks/router';

const KINDS: Array<{ kind: string; title: string; icon: string }> = [
  { kind: 'app-observability', title: 'App Observability', icon: 'lucide-app-window' },
  { kind: 'blackbox', title: 'Blackbox probes', icon: 'lucide-radar' },
  { kind: 'cluster-metrics', title: 'Cluster metrics', icon: 'lucide-boxes' },
  { kind: 'database', title: 'Database', icon: 'lucide-database' },
  { kind: 'pod-logs', title: 'Pod logs', icon: 'lucide-scroll-text' },
  { kind: 'self-monitoring', title: 'Self-monitoring', icon: 'lucide-activity' },
  { kind: 'future-wizard', title: 'Zz future wizard', icon: 'lucide-wand' },
];

test('each wizard card has its own icon; an unknown kind gets the generic wand', async ({
  page,
  api,
}) => {
  await api.loginAs(orgAdmin);
  api.seed({ orgs: [basicScenario().org] });
  api.override('POST', '/shepherd.mgmt.v1.WizardService/ListWizards', (route) =>
    json(route, 200, {
      items: KINDS.map((k) => ({ kind: k.kind, title: k.title, description: k.kind, steps: [] })),
      total: KINDS.length,
    }),
  );
  await page.goto('/wizards');

  const seen = new Set<string>();
  for (const k of KINDS) {
    const card = page.locator('div').filter({ has: page.getByRole('heading', { name: k.title }) });
    const svg = card.last().locator('svg').first();
    await expect(svg).toHaveClass(new RegExp(`\\b${k.icon}\\b`));
    const cls = (await svg.getAttribute('class')) ?? '';
    const iconClass = cls.split(/\s+/).find((c) => c.startsWith('lucide-')) ?? '';
    seen.add(iconClass);
  }
  // Distinct per card, not just "an icon is present".
  expect(seen.size).toBe(KINDS.length);
});
