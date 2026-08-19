import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';

// Regression guard for docs/visual-builder-refinement.md B1: the visual
// builder used Tailwind utility classes (bg-card, bg-background, bg-accent,
// text-muted-foreground, ...) that no @theme defined, so they compiled to
// zero CSS. This test locks in the token layer and makes sure nobody
// reintroduces a class that resolves to nothing.

const CSS_PATH = join(__dirname, 'index.css');
const VISUAL_DIR = join(__dirname, 'visual');

// The exact token set + hex values from the mockup (B1).
const EXPECTED_TOKENS: Record<string, string> = {
  '--color-background': '#09090b',
  '--color-panel': '#0e0e11',
  '--color-card': '#18181b',
  '--color-border': '#27272a',
  '--color-border-strong': '#3f3f46',
  '--color-muted': '#a1a1aa',
  '--color-muted-2': '#71717a',
  '--color-accent': '#6366f1',
};

// Utility-class name fragments that must never appear in src/visual/ because
// no @theme token backs them — either because they were never migrated, or
// because a shadcn/ui-style name (e.g. `-foreground`, `primary`, `popover`)
// crept back in without a matching token being added above.
const PHANTOM_CLASS_PATTERN =
  /\b(?:bg|text|border|ring|from|to|via|placeholder|divide|outline|shadow|decoration)-(?:muted-foreground|foreground|card-foreground|primary(?:-foreground)?|secondary(?:-foreground)?|destructive(?:-foreground)?|popover(?:-foreground)?|input)\b/;

function readCss(): string {
  return readFileSync(CSS_PATH, 'utf8');
}

function extractThemeBlock(css: string): string {
  const start = css.indexOf('@theme');
  expect(start, '@theme block not found in src/index.css').toBeGreaterThanOrEqual(0);
  const braceStart = css.indexOf('{', start);
  let depth = 0;
  let i = braceStart;
  for (; i < css.length; i++) {
    if (css[i] === '{') depth++;
    else if (css[i] === '}') {
      depth--;
      if (depth === 0) break;
    }
  }
  return css.slice(braceStart + 1, i);
}

function collectSourceFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    const st = statSync(full);
    if (st.isDirectory()) {
      out.push(...collectSourceFiles(full));
    } else if (/\.(tsx|ts)$/.test(entry) && !/\.test\.(tsx|ts)$/.test(entry)) {
      out.push(full);
    }
  }
  return out;
}

describe('design token layer (index.css @theme)', () => {
  const css = readCss();
  const theme = extractThemeBlock(css);

  it.each(Object.entries(EXPECTED_TOKENS))('defines %s as %s', (name, hex) => {
    const re = new RegExp(`${name}\\s*:\\s*${hex}\\b`, 'i');
    expect(theme).toMatch(re);
  });
});

describe('no phantom Tailwind classes in web/src/visual/', () => {
  const files = collectSourceFiles(VISUAL_DIR);

  it('found visual source files to scan', () => {
    expect(files.length).toBeGreaterThan(0);
  });

  it.each(files.map((f) => [f.replace(`${VISUAL_DIR}/`, ''), f] as const))(
    '%s has no class that resolves to no CSS',
    (_label, file) => {
      const src = readFileSync(file, 'utf8');
      const match = src.match(PHANTOM_CLASS_PATTERN);
      expect(match, `found phantom class "${match?.[0]}" in ${file}`).toBeNull();
    },
  );
});
