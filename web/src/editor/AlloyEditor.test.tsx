// @vitest-environment jsdom
import { forEachDiagnostic } from '@codemirror/lint';
import { EditorView } from '@codemirror/view';
import { act, render } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { Diagnostic } from '@/gen/shepherd/mgmt/v1/common_pb';
import { AlloyEditor, toCmDiagnostics } from './AlloyEditor';

// M5 (2026-10-08 walkthrough): a server diagnostic is computed for the text
// that was validated. When the buffer is replaced by shorter text (Format, a
// restore, a refetch) before the next validation lands, the stale "6:15"
// pointed past the end of the new document and CodeMirror threw
// `RangeError: Invalid position 93 in document of length 80`.

const diag = (line: number, col: number, message = 'regexp'): Diagnostic =>
  ({ line, col, message, stage: 2 }) as Diagnostic;

// Six lines; the sixth is short, so 6:15 is past its end AND past the end of
// the whole document (75 + 14 = 89 > 80).
const shortDoc = `${'abcdefghijklmn\n'.repeat(5)}xxxxx`;
const longDoc = `${'abcdefghijklmn\n'.repeat(5)}prometheus.relabel "a" { regex = "(" }\n`;

function viewOf(container: HTMLElement): EditorView {
  const dom = container.querySelector('.cm-editor') as HTMLElement | null;
  if (!dom) throw new Error('no editor mounted');
  const view = EditorView.findFromDOM(dom);
  if (!view) throw new Error('no editor view');
  return view;
}

const noop = () => undefined;

const settle = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 1000));
  });

afterEach(() => {
  vi.restoreAllMocks();
});

describe('toCmDiagnostics', () => {
  it('keeps every range inside the document and inside its line', () => {
    expect(shortDoc.length).toBe(80);
    const view = new EditorView({ doc: shortDoc });
    const out = toCmDiagnostics(view.state.doc, [diag(6, 15), diag(99, 3), diag(2, 400)]);
    for (const d of out) {
      expect(d.from).toBeGreaterThanOrEqual(0);
      expect(d.to).toBeGreaterThanOrEqual(d.from);
      expect(d.to).toBeLessThanOrEqual(view.state.doc.length);
    }
    view.destroy();
  });
});

describe('AlloyEditor diagnostics', () => {
  // The diagnostics used to reach CodeMirror through a `linter()` source plus
  // forceLinting — and forceLinting is a no-op once the linter has no run
  // pending, so diagnostics arriving after the first lint (the server answer,
  // ~1s after load) never drew a squiggle or a gutter marker.
  it('draws diagnostics that arrive after the editor has settled', async () => {
    const { container, rerender } = render(
      <AlloyEditor value={longDoc} onChange={noop} diagnostics={[]} />,
    );
    await settle();
    const reported = [diag(6, 3)];
    rerender(<AlloyEditor value={longDoc} onChange={noop} diagnostics={reported} />);
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    const view = viewOf(container);
    const seen: { from: number; message: string }[] = [];
    forEachDiagnostic(view.state, (d, from) => seen.push({ from, message: d.message }));
    expect(seen).toEqual([{ from: view.state.doc.line(6).from + 2, message: 'regexp' }]);

    // A wholesale replacement drops them: they were positions in the old text
    // (the same array still held by the page until its next validation).
    rerender(<AlloyEditor value={shortDoc} onChange={noop} diagnostics={reported} />);
    await act(async () => {
      await new Promise((r) => setTimeout(r, 50));
    });
    let left = 0;
    forEachDiagnostic(viewOf(container).state, () => left++);
    expect(left).toBe(0);
  });

  it('does not throw when a diagnostic lies beyond a shorter replacement document', async () => {
    const errors: unknown[] = [];
    vi.spyOn(console, 'error').mockImplementation((...a) => errors.push(a));
    const onError = (e: ErrorEvent) => errors.push(e.error ?? e.message);
    window.addEventListener('error', onError);
    // CodeMirror's lint plugin applies results in a promise chain, so the
    // RangeError surfaces as an unhandled rejection, not a synchronous throw.
    const onRejection = (reason: unknown) => errors.push(reason);
    process.on('unhandledRejection', onRejection);

    const diagnostics = [diag(6, 15)];
    const { container, rerender } = render(
      <AlloyEditor value={longDoc} onChange={noop} diagnostics={diagnostics} />,
    );
    await settle();
    // The page still holds the old diagnostics when the buffer is replaced.
    rerender(<AlloyEditor value={shortDoc} onChange={noop} diagnostics={diagnostics} />);
    await settle();

    const view = viewOf(container);
    expect(view.state.doc.toString()).toBe(shortDoc);
    forEachDiagnostic(view.state, (_d, from, to) => {
      expect(to).toBeLessThanOrEqual(view.state.doc.length);
      expect(from).toBeLessThanOrEqual(to);
    });
    window.removeEventListener('error', onError);
    process.off('unhandledRejection', onRejection);
    expect(errors).toEqual([]);
  });
});
