/**
 * @vitest-environment jsdom
 */
import { readdirSync, readFileSync, statSync } from 'node:fs';
import { join } from 'node:path';
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { Modal, ModalActions } from './Modal';

// vitest.config.ts does not set `test.globals: true`, so @testing-library/react's
// automatic afterEach cleanup (which detects a global `afterEach`) never fires —
// without this every test after the first would find more than one dialog.
afterEach(cleanup);

// S2: components/ui/Modal is the one overlay implementation. It must carry
// forward every accessibility guarantee AdminModal.tsx had (role=dialog,
// aria-modal, Escape-to-close, focus trap) plus the new size/testId knobs
// the migrated pages need.
describe('Modal', () => {
  it('renders a labelled dialog', () => {
    render(
      <Modal title='New destination' onClose={() => undefined}>
        body
      </Modal>,
    );
    expect(screen.getByRole('dialog', { name: 'New destination' })).not.toBeNull();
  });

  it('is aria-modal', () => {
    render(
      <Modal title='X' onClose={() => undefined}>
        body
      </Modal>,
    );
    expect(screen.getByRole('dialog').getAttribute('aria-modal')).toBe('true');
  });

  it('calls onClose when Escape is pressed', () => {
    const onClose = vi.fn();
    render(
      <Modal title='X' onClose={onClose}>
        body
      </Modal>,
    );
    fireEvent.keyDown(document, { key: 'Escape' });
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('applies a data-testid to the dialog when given', () => {
    render(
      <Modal title='X' onClose={() => undefined} testId='destination-modal'>
        body
      </Modal>,
    );
    expect(screen.getByTestId('destination-modal')).not.toBeNull();
  });

  it('sizes the panel from the size prop', () => {
    render(
      <Modal title='X' onClose={() => undefined} size='xl'>
        body
      </Modal>,
    );
    expect(screen.getByRole('dialog').className).toMatch(/max-w-xl/);
  });

  // F1/S1: a page that keeps its form state in the page passes a brand-new
  // `onClose` arrow on every re-render (one per keystroke, since the state
  // setter that updates the input also redefines the closure). Before the
  // fix, the focus-trap effect was keyed on [onClose], so every one of those
  // re-renders re-ran panelRef.current?.focus() and yanked focus back to the
  // dialog panel, out of the input the user was typing into — only the
  // first character ever landed. The effect must run once per mount, not
  // once per onClose identity.
  it('keeps focus in an input across a re-render that passes a new onClose', () => {
    const { rerender } = render(
      <Modal title='X' onClose={() => undefined}>
        <input aria-label='Name' />
      </Modal>,
    );
    const input = screen.getByLabelText('Name');
    input.focus();
    expect(document.activeElement).toBe(input);

    // Re-render with a brand new arrow function identity for onClose, the
    // same shape a page-owned dialog produces on every keystroke.
    rerender(
      <Modal title='X' onClose={() => undefined}>
        <input aria-label='Name' />
      </Modal>,
    );

    expect(document.activeElement).toBe(input);
  });
});

describe('ModalActions', () => {
  it('tags the submit button with submitTestId', () => {
    render(
      <ModalActions
        onCancel={() => undefined}
        submitLabel='Save'
        pendingLabel='Saving…'
        pending={false}
        submitTestId='save-destination'
      />,
    );
    expect(screen.getByTestId('save-destination').textContent).toBe('Save');
  });
});

// W5: after S2 promoted AdminModal into the one shared components/ui/Modal,
// the three visual-builder overlays (GraphViewPage's recreate-confirm,
// UpgradeReview, SandboxRunPanel) still hand-rolled their own
// `fixed inset-0 ... bg-black/NN` backdrop instead of using it — this widens
// S2's own drift guard to scan web/src/visual too, recursively, so a NEW
// overlay slipping back in outside Modal fails here, not just the three
// files named in the W5 plan. SandboxRunPanel additionally hand-rolled two
// `<table>` shells that belong on components/ui/DataTable (S3) — same scan,
// same reasoning.
const VISUAL_DIR = join(__dirname, '..', '..', 'visual');
const BACKDROP_LITERAL_RE = /fixed inset-0[^'"]*bg-black\/\d/;

function collectTsxFiles(dir: string): string[] {
  const out: string[] = [];
  for (const entry of readdirSync(dir)) {
    if (entry.endsWith('.test.tsx') || entry.endsWith('.test.ts')) continue;
    const p = join(dir, entry);
    const st = statSync(p);
    if (st.isDirectory()) out.push(...collectTsxFiles(p));
    else if (entry.endsWith('.tsx')) out.push(p);
  }
  return out;
}

// Resolved once at module load — vitest re-imports this file per run, so a
// file added or migrated under web/src/visual is picked up automatically,
// with no list to keep in sync by hand (unlike primitives.test.ts's
// intentionally-hand-curated PAGES lists, which name a fixed migration set).
const VISUAL_TSX_FILES = collectTsxFiles(VISUAL_DIR);

describe('visual overlay migration (W5)', () => {
  it.each(VISUAL_TSX_FILES)('%s does not hand-roll a fixed-inset backdrop', (file) => {
    expect(readFileSync(file, 'utf8')).not.toMatch(BACKDROP_LITERAL_RE);
  });

  it.each(VISUAL_TSX_FILES)('%s does not hand-roll a <table> shell', (file) => {
    expect(readFileSync(file, 'utf8')).not.toContain('<table');
  });
});
