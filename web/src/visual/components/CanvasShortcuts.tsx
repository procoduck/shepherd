import { Keyboard } from 'lucide-react';
import { Modal } from '../../components/ui/Modal';
import { CANVAS_SHORTCUTS } from '../canvasA11y';

/**
 * The canvas's keyboard help (#426): a button in the canvas's top-right corner
 * and the `?` key both open the shortcut sheet, which lists CANVAS_SHORTCUTS —
 * the same table the docs page carries — so what the sheet says is what
 * useCanvasKeyboard does.
 */
export function CanvasShortcutsButton({ onOpen }: { onOpen: () => void }) {
  return (
    <button
      type='button'
      data-testid='canvas-shortcuts-button'
      aria-label='Keyboard shortcuts'
      aria-keyshortcuts='?'
      title='Keyboard shortcuts (?)'
      onClick={onOpen}
      className='absolute top-3 right-3 z-10 flex items-center gap-1 rounded border border-border bg-card px-2 py-1 text-xs text-muted hover:text-zinc-100'
    >
      <Keyboard size={14} aria-hidden='true' />
      <span aria-hidden='true'>?</span>
    </button>
  );
}

const slug = (group: string) => `canvas-shortcuts-${group.toLowerCase().replace(/[^a-z]+/g, '-')}`;

export function CanvasShortcutsDialog({ onClose }: { onClose: () => void }) {
  return (
    <Modal title='Canvas keyboard shortcuts' onClose={onClose} size='xl' testId='canvas-shortcuts'>
      <div className='space-y-4 text-sm'>
        <p className='text-muted'>
          Tab into the canvas, then use the arrow keys to reach its nodes. A focused node is
          announced by name; Space selects it and I opens it in the Properties panel.
        </p>
        {CANVAS_SHORTCUTS.map((group) => (
          <section key={group.group} aria-labelledby={slug(group.group)}>
            <h3 id={slug(group.group)} className='mb-1 font-semibold'>
              {group.group}
            </h3>
            <dl className='grid grid-cols-[14rem_1fr] gap-x-3'>
              {group.items.map((item) => (
                <div key={`${item.keys}-${item.action}`} className='contents'>
                  <dt className='border-t border-border py-1'>
                    <kbd className='font-mono text-xs'>{item.keys}</kbd>
                  </dt>
                  <dd className='border-t border-border py-1 text-muted'>{item.action}</dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </div>
    </Modal>
  );
}

/** The polite live region keyboard actions are announced through. `seq`
 *  changes on every announcement, so the same text twice is still read. */
export function CanvasLiveRegion({ text, seq }: { text: string; seq: number }) {
  return (
    <div role='status' aria-live='polite' className='sr-only' data-testid='canvas-announcer'>
      {text}
      {seq % 2 === 1 ? ' ' : ''}
    </div>
  );
}
