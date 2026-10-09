import type { Key, MouseEvent, ReactNode } from 'react';

/** What inside a row handles its own click — a row click never doubles it. */
const ROW_INNER_CONTROLS =
  'a, button, input, select, textarea, label, summary, [role="button"], [role="switch"], [role="link"]';

/**
 * Whether a click on a row means "open this row": a plain primary click that
 * did not land on a control of its own (a link, a toggle) and did not end a
 * text selection — someone selecting a cluster name to copy it is not asking
 * to leave the page. A modified click (new tab, etc.) is left to the row's own
 * link. Known trade-off: a double-click to select a word navigates on its
 * first click (no selection exists yet); drag-selecting text works.
 */
export function isRowClickNavigation(e: MouseEvent<HTMLElement>): boolean {
  if (e.defaultPrevented || e.button !== 0) return false;
  if (e.metaKey || e.ctrlKey || e.shiftKey || e.altKey) return false;
  const target = e.target as Element | null;
  if (target?.closest?.(ROW_INNER_CONTROLS)) return false;
  const selection = typeof window !== 'undefined' ? window.getSelection() : null;
  if (selection && !selection.isCollapsed && selection.toString().trim() !== '') return false;
  return true;
}

/**
 * The one table shell for the app (S3). Every page-level table repeated the
 * same border/thead/row markup by hand — this is that markup, parameterised
 * by columns instead of copied.
 *
 * Row styling varies across the pages that migrate onto this (some rows are
 * clickable and get a hover + pointer cursor, some are plain), so
 * `rowClassName` is a full override rather than an additive modifier —
 * getting a page's exact prior look back is what "no behavioural change"
 * means here.
 */
export interface DataTableColumn<T> {
  key: string;
  header: ReactNode;
  headerClassName?: string;
  cellClassName?: string;
  /**
   * A fixed column width (any CSS length, e.g. '12%' or '8rem'). When any
   * column sets one the table switches to `table-layout: fixed`, so several
   * DataTables rendered one under another with the same columns (grouped
   * views) line their columns up instead of each sizing to its own content.
   */
  width?: string;
  render: (row: T) => ReactNode;
}

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  rowClassName = 'border-t border-border hover:bg-card/60',
  rowProps,
  onRowClick,
  scrollX,
  testId,
  ariaLabelledBy,
}: {
  columns: DataTableColumn<T>[];
  rows: T[];
  rowKey: (row: T) => Key;
  /** Full class string applied to every <tr>; default matches the app's original hover row. */
  rowClassName?: string | ((row: T) => string);
  /** Extra attributes (data-testid, onClick, ...) merged onto each <tr>. */
  rowProps?: (row: T) => Record<string, unknown>;
  /**
   * Makes the whole row a click target (e.g. "open this collector"). Mouse
   * only: the row is not focusable — keyboard users reach the same place
   * through the link the row already holds, which stays the one focusable
   * element. See `isRowClickNavigation` for the clicks it leaves alone.
   */
  onRowClick?: (row: T) => void;
  scrollX?: boolean;
  testId?: string;
  ariaLabelledBy?: string;
}) {
  const fixed = columns.some((c) => c.width !== undefined);
  return (
    <div
      data-testid={testId}
      className={`rounded-lg border border-border overflow-hidden${scrollX ? ' overflow-x-auto' : ''}`}
    >
      <table
        className={`w-full text-sm${fixed ? ' table-fixed' : ''}`}
        aria-labelledby={ariaLabelledBy}
      >
        {fixed && (
          <colgroup>
            {columns.map((c) => (
              <col key={c.key} style={c.width ? { width: c.width } : undefined} />
            ))}
          </colgroup>
        )}
        <thead className='bg-card text-muted'>
          <tr>
            {columns.map((c) => (
              <th key={c.key} className={c.headerClassName ?? 'px-4 py-3 text-left font-medium'}>
                {c.header}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {rows.map((row) => {
            const extra = rowProps?.(row) ?? {};
            const cls = typeof rowClassName === 'function' ? rowClassName(row) : rowClassName;
            return (
              <tr
                key={rowKey(row)}
                className={cls}
                onClick={
                  onRowClick
                    ? (e) => {
                        if (isRowClickNavigation(e)) onRowClick(row);
                      }
                    : undefined
                }
                {...extra}
              >
                {columns.map((c) => (
                  <td key={c.key} className={c.cellClassName ?? 'px-4 py-2.5'}>
                    {c.render(row)}
                  </td>
                ))}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
