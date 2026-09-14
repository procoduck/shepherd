import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import {
  applyTheme,
  currentTheme,
  getStoredTheme,
  getSystemTheme,
  resolveTheme,
  setStoredTheme,
} from './theme';

// Regression guard for docs/archive/visual-builder-refinement.md B1: the visual
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
// This is the DARK table — the default @theme block. D8 (light mode, W6-S1)
// adds a second, LIGHT table below: the same token names, redefined under
// `html.light` and `@media (prefers-color-scheme: light) { :root:not(.dark) }`.
const EXPECTED_TOKENS_DARK: Record<string, string> = {
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

// Light palette (D8). Contrast-checked (WCAG relative-luminance formula)
// against both --color-background and --color-card: muted/muted-2/muted-3
// all clear 4.5:1 (the AA text floor); the palette mirrors the dark table's
// token semantics (background is the extreme shade, card the raised
// surface) rather than being a literal hue-preserving invert.
// F2 (2026-09-14 walkthrough fixes) adds --color-zinc-100/200/300: these
// are Tailwind's own tokens, redefined here (not renamed to a project token)
// because text-zinc-100/200/300 are the deliberately un-tokenized
// "foreground" shades used raw by <body>/Shell — see RAW_ZINC_ALLOWLIST
// below. Redefining them in both light blocks is what flips the base
// foreground (and everything that inherits it) in light mode.
const EXPECTED_TOKENS_LIGHT: Record<string, string> = {
  '--color-background': '#fafafa',
  '--color-panel': '#f4f4f5',
  '--color-card': '#ffffff',
  '--color-border': '#e4e4e7',
  '--color-border-strong': '#d4d4d8',
  '--color-muted': '#3f3f46',
  '--color-muted-2': '#52525b',
  '--color-muted-3': '#71717a',
  '--color-accent': '#4f46e5',
  '--color-zinc-100': '#18181b',
  '--color-zinc-200': '#27272a',
  '--color-zinc-300': '#3f3f46',
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

// text-zinc-100/200/300 are deliberately NOT tokenized under a new name:
// they are the "full brightness" foreground text used for headings,
// hover/active emphasis, and (via inheritance) the base body/Shell text
// colour. The design system has no generic foreground/emphasis token by
// design (see the PHANTOM_CLASS_PATTERN ban on `-foreground` names below —
// this project never introduced a shadcn-style `text-foreground`), and the
// app's base text color is set on <body> in index.html (outside this token
// layer) as `text-zinc-100`, so introducing a differently-named token for
// the same role here would just fragment one convention into two. Instead
// (F2, 2026-09-14 walkthrough fixes) index.css redefines Tailwind's own
// --color-zinc-100/200/300 custom properties directly inside both light
// blocks (html.light and :root:not(.dark) under prefers-color-scheme:
// light) — see EXPECTED_TOKENS_LIGHT above — so these three utilities are
// theme-flipped in place without a rename. Any other raw zinc-* class is a
// real regression and must be mapped to a token instead of added here.
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

// Extracts the balanced-brace body of the first `{...}` found after `marker`
// in css (a literal substring search, e.g. '@theme' or 'html.light'). Works
// for a rule nested inside another block (e.g. `:root:not(.dark)` inside an
// `@media` block) because brace-depth counting starts fresh at the found `{`.
function extractBlockAfter(css: string, marker: string, label: string): string {
  const start = css.indexOf(marker);
  expect(start, `"${marker}" not found in src/index.css (${label})`).toBeGreaterThanOrEqual(0);
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

function extractThemeBlock(css: string): string {
  return extractBlockAfter(css, '@theme', 'the dark @theme block');
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

describe('design token layer (index.css @theme, dark default)', () => {
  const css = readCss();
  const theme = extractThemeBlock(css);

  it.each(Object.entries(EXPECTED_TOKENS_DARK))('defines %s as %s', (name, hex) => {
    const re = new RegExp(`${name}\\s*:\\s*${hex}\\b`, 'i');
    expect(theme).toMatch(re);
  });
});

// D8 (light mode, W6-S1). Two places redefine the same token names for
// light: an explicit override (`html.light`, wins regardless of OS
// preference — the toggle sets this class) and the system-preference
// default (`:root:not(.dark)` nested inside `@media (prefers-color-scheme:
// light)` — the ":not(.dark)" guard is what lets an explicit dark override
// beat a light OS preference). Both must carry the identical light values,
// or which one wins would depend on CSS specificity/source-order instead of
// being deliberate.
describe('design token layer (index.css light overrides)', () => {
  const css = readCss();

  it('defines a light override guarded by prefers-color-scheme and :not(.dark)', () => {
    expect(css).toMatch(/@media\s*\(prefers-color-scheme:\s*light\)/);
    expect(css).toMatch(/:root:not\(\.dark\)/);
  });

  describe('html.light (explicit toggle override)', () => {
    const block = extractBlockAfter(css, 'html.light', 'the html.light override rule');
    it.each(Object.entries(EXPECTED_TOKENS_LIGHT))('defines %s as %s', (name, hex) => {
      const re = new RegExp(`${name}\\s*:\\s*${hex}\\b`, 'i');
      expect(block).toMatch(re);
    });
  });

  describe(':root:not(.dark) under @media(prefers-color-scheme: light) (system default)', () => {
    const block = extractBlockAfter(css, ':root:not(.dark)', 'the prefers-color-scheme block');
    it.each(Object.entries(EXPECTED_TOKENS_LIGHT))('defines %s as %s', (name, hex) => {
      const re = new RegExp(`${name}\\s*:\\s*${hex}\\b`, 'i');
      expect(block).toMatch(re);
    });
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

// D8 (light mode, W6-S1). theme.ts's resolution helpers, exercised against
// minimal window/document stand-ins — same approach as
// src/hooks/useOrg.test.ts's localStorage round-trip (no jsdom in this
// project's vitest setup for plain .test.ts files).
describe('theme resolution helpers (src/theme.ts)', () => {
  class MemoryStorage {
    private store = new Map<string, string>();
    getItem(key: string) {
      return this.store.has(key) ? this.store.get(key)! : null;
    }
    setItem(key: string, value: string) {
      this.store.set(key, value);
    }
    removeItem(key: string) {
      this.store.delete(key);
    }
    clear() {
      this.store.clear();
    }
  }

  class FakeClassList {
    private names = new Set<string>();
    contains(name: string) {
      return this.names.has(name);
    }
    toggle(name: string, force?: boolean) {
      const on = force ?? !this.names.has(name);
      if (on) this.names.add(name);
      else this.names.delete(name);
      return on;
    }
  }

  let storage: MemoryStorage;
  let classList: FakeClassList;
  let systemPrefersLight: boolean;

  beforeEach(() => {
    storage = new MemoryStorage();
    classList = new FakeClassList();
    systemPrefersLight = false;
    (globalThis as unknown as { window: unknown }).window = {
      localStorage: storage,
      matchMedia: (query: string) => ({
        get matches() {
          // Only the one media feature theme.ts queries.
          return query === '(prefers-color-scheme: light)' && systemPrefersLight;
        },
      }),
    };
    (globalThis as unknown as { document: unknown }).document = {
      documentElement: { classList },
    };
  });

  afterEach(() => {
    delete (globalThis as unknown as { window?: unknown }).window;
    delete (globalThis as unknown as { document?: unknown }).document;
  });

  it('getStoredTheme is null when nothing was ever stored', () => {
    expect(getStoredTheme()).toBeNull();
  });

  it('getStoredTheme ignores a non-theme value under the key', () => {
    storage.setItem('theme', 'purple');
    expect(getStoredTheme()).toBeNull();
  });

  it('setStoredTheme/getStoredTheme round-trip under the documented key', () => {
    setStoredTheme('light');
    expect(storage.getItem('theme')).toBe('light');
    expect(getStoredTheme()).toBe('light');
  });

  it('getSystemTheme reflects prefers-color-scheme: light', () => {
    systemPrefersLight = true;
    expect(getSystemTheme()).toBe('light');
  });

  it('getSystemTheme defaults to dark when the OS has no light preference', () => {
    systemPrefersLight = false;
    expect(getSystemTheme()).toBe('dark');
  });

  it('resolveTheme prefers the stored choice over the system preference', () => {
    systemPrefersLight = true; // system says light...
    setStoredTheme('dark'); // ...but the user explicitly chose dark
    expect(resolveTheme()).toBe('dark');
  });

  it('resolveTheme falls back to the system preference with no stored choice', () => {
    systemPrefersLight = true;
    expect(getStoredTheme()).toBeNull();
    expect(resolveTheme()).toBe('light');
  });

  it('applyTheme sets html.light and clears html.dark', () => {
    classList.toggle('dark', true);
    applyTheme('light');
    expect(classList.contains('light')).toBe(true);
    expect(classList.contains('dark')).toBe(false);
  });

  it('applyTheme sets html.dark and clears html.light', () => {
    classList.toggle('light', true);
    applyTheme('dark');
    expect(classList.contains('dark')).toBe(true);
    expect(classList.contains('light')).toBe(false);
  });

  it('currentTheme reads back an explicit class over the system preference', () => {
    systemPrefersLight = true;
    applyTheme('dark');
    expect(currentTheme()).toBe('dark');
  });

  it('currentTheme falls back to the system preference with neither class set', () => {
    systemPrefersLight = true;
    expect(classList.contains('light')).toBe(false);
    expect(classList.contains('dark')).toBe(false);
    expect(currentTheme()).toBe('light');
  });
});
