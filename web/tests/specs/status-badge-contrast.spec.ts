/**
 * #253: status badges were Tailwind's -400 hues on a 10-15% tint of the same
 * hue — readable on the dark surfaces, 1.4-2.5:1 on the light ones. They now
 * use the contrast-checked status tokens in index.css. This measures the
 * rendered badges (computed colours, composited over whatever they sit on) in
 * both themes, so a token that drifts or a badge that falls back to a raw hue
 * fails here rather than only in theme.test.ts's arithmetic.
 */
import type { Locator, Page } from '@playwright/test';
import { basicScenario } from '../fixtures/factories';
import { orgAdmin } from '../fixtures/personas';
import { expect, test } from '../fixtures/test';

// Contrast of an element's text against its effective background: walk up to
// the first opaque ancestor background, paint the translucent layers over it
// on a canvas (which also normalises oklch()/color-mix() computed values to
// sRGB), and compare that pixel with the text colour painted over it.
async function contrast(el: Locator): Promise<number> {
  return el.evaluate((node) => {
    const layers: string[] = [];
    let cur: Element | null = node;
    while (cur) {
      const bg = getComputedStyle(cur).backgroundColor;
      layers.unshift(bg);
      const probe = document.createElement('canvas').getContext('2d')!;
      probe.clearRect(0, 0, 1, 1);
      probe.fillStyle = bg;
      probe.fillRect(0, 0, 1, 1);
      if (probe.getImageData(0, 0, 1, 1).data[3] === 255) break;
      cur = cur.parentElement;
    }
    const ctx = document.createElement('canvas').getContext('2d')!;
    const paint = (colors: string[]) => {
      ctx.clearRect(0, 0, 1, 1);
      ctx.fillStyle = '#ffffff';
      ctx.fillRect(0, 0, 1, 1);
      for (const c of colors) {
        ctx.fillStyle = c;
        ctx.fillRect(0, 0, 1, 1);
      }
      return Array.from(ctx.getImageData(0, 0, 1, 1).data.slice(0, 3));
    };
    const bg = paint(layers);
    const fg = paint([...layers, getComputedStyle(node).color]);
    const lum = ([r, g, b]: number[]) => {
      const f = (v: number) => {
        const c = v / 255;
        return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
      };
      return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
    };
    const [a, b] = [lum(fg), lum(bg)];
    return (Math.max(a, b) + 0.05) / (Math.min(a, b) + 0.05);
  });
}

async function badges(page: Page): Promise<Locator[]> {
  const table = page.getByRole('table');
  return Promise.all(
    ['APPLIED', 'APPLYING', 'FAILED', 'UNKNOWN'].map(async (s) => {
      const b = table.getByText(s, { exact: true });
      await expect(b).toBeVisible();
      return b;
    }),
  );
}

for (const scheme of ['light', 'dark'] as const) {
  test(`collector status badges meet WCAG AA contrast in ${scheme} mode`, async ({ page, api }) => {
    await api.loginAs(orgAdmin);
    const s = basicScenario();
    s.collectors[0].remote_config_status = 'APPLIED';
    s.collectors[1].remote_config_status = 'APPLYING';
    s.collectors[2].remote_config_status = 'FAILED';
    s.collectors[3].remote_config_status = '';
    api.seed({ orgs: [s.org], collectors: s.collectors });
    await page.emulateMedia({ colorScheme: scheme });
    await page.goto('/collectors');
    for (const b of await badges(page)) {
      const ratio = await contrast(b);
      expect(ratio, `${await b.innerText()} badge in ${scheme} mode`).toBeGreaterThanOrEqual(4.5);
    }
  });
}
