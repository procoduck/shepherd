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

// Directories outside web/src/visual/ that were migrated onto the token layer
// in B4 (docs/project-status.md) and must not regress back to raw zinc-*
// utilities. Kept as a separate list (rather than merging into VISUAL_DIR)
// because each entry is scanned by both the phantom-class check below and
// the raw-zinc-class check further down.
const MIGRATED_DIRS = [
  join(__dirname, 'pages'),
  join(__dirname, 'components'),
  join(__dirname, 'editor'),
  join(__dirname, 'hooks'),
];

// The exact token set + hex values from the mockup (B1), plus tokens added
// while migrating web/src/pages/ and web/src/components/ onto the layer (B4).
const EXPECTED_TOKENS: Record<string, string> = {
  '--color-background': '#09090b',
  '--color-panel': '#0e0e11',
  '--color-card': '#18181b',
  '--color-border': '#27272a',
  '--color-border-strong': '#3f3f46',
  '--color-muted': '#a1a1aa',
  '--color-muted-2': '#71717a',
  '--color-muted-3': '#52525b',
  '--color-accent': '#6366f1',
};

// Utility-class fragments that resolve to a real Tailwind zinc-scale color
// rather than an @theme token. Every one of these has a token equivalent
// (bg-zinc-950 -> bg-background, bg-zinc-900 -> bg-card, bg-zinc-800 ->
// bg-border, bg-zinc-700 -> bg-border-strong, text-zinc-400 -> text-muted,
// text-zinc-500 -> text-muted-2, text-zinc-600 -> text-muted-3) EXCEPT the
// three near-white "foreground" shades in RAW_ZINC_ALLOWLIST below, which
// intentionally have no token (see comment there).
const RAW_ZINC_PATTERN =
  /\b(?:bg|text|border|ring|from|to|via|placeholder|divide|outline|shadow|decoration|fill|stroke)-zinc-\d{2,3}(?:\/\d{1,3})?\b/g;

// text-zinc-100/200/300 are deliberately NOT tokenized: they are near-white
// "full brightness" text used for headings and hover/active emphasis. The
// design system has no generic foreground/emphasis token by design (see the
// PHANTOM_CLASS_PATTERN ban on `-foreground` names below — this project
// never introduced a shadcn-style `text-foreground`), and the app's base
// text color is set on <body> in index.html (outside this token layer) as
// `text-zinc-100`, so introducing a differently-named token for the same
// role here would just fragment one convention into two. Any other raw
// zinc-* class is a real regression and must be mapped to a token instead
// of added here.
const RAW_ZINC_ALLOWLIST = new Set(['text-zinc-100', 'text-zinc-200', 'text-zinc-300']);

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

// Regression guard for docs/project-status.md B4: web/src/pages/ and
// web/src/components/ used 126 raw zinc-* classes and zero token classes,
// so the two halves of the app could drift the moment a token value
// changed. Both checks below now cover pages/, components/, editor/ and
// hooks/ (this task's ownership) as well as visual/.
const MIGRATED_FILES = MIGRATED_DIRS.flatMap((dir) =>
  collectSourceFiles(dir).map((f) => [f.replace(`${__dirname}/`, ''), f] as const),
);

describe('no phantom Tailwind classes in migrated app dirs', () => {
  it('found migrated source files to scan', () => {
    expect(MIGRATED_FILES.length).toBeGreaterThan(0);
  });

  it.each(MIGRATED_FILES)('%s has no class that resolves to no CSS', (_label, file) => {
    const src = readFileSync(file, 'utf8');
    const match = src.match(PHANTOM_CLASS_PATTERN);
    expect(match, `found phantom class "${match?.[0]}" in ${file}`).toBeNull();
  });
});

describe('no un-tokenized raw zinc-* classes in migrated app dirs', () => {
  it.each(MIGRATED_FILES)('%s has no raw zinc-* class outside the allowlist', (_label, file) => {
    const src = readFileSync(file, 'utf8');
    const found = [...src.matchAll(RAW_ZINC_PATTERN)].map((m) => m[0]);
    const unexpected = found.filter((cls) => !RAW_ZINC_ALLOWLIST.has(cls));
    expect(
      unexpected,
      `found raw zinc class(es) ${JSON.stringify(unexpected)} in ${file} with no @theme token — ` +
        'map it to a token, or add it to RAW_ZINC_ALLOWLIST with a reason if it must stay raw',
    ).toEqual([]);
  });
});
