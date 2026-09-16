import type { Key, ReactNode } from 'react';

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
  render: (row: T) => ReactNode;
}

export function DataTable<T>({
  columns,
  rows,
  rowKey,
  rowClassName = 'border-t border-border hover:bg-card/60',
  rowProps,
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
  scrollX?: boolean;
  testId?: string;
  ariaLabelledBy?: string;
}) {
  return (
    <div
      data-testid={testId}
      className={`rounded-lg border border-border overflow-hidden${scrollX ? ' overflow-x-auto' : ''}`}
    >
      <table className='w-full text-sm' aria-labelledby={ariaLabelledBy}>
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
              <tr key={rowKey(row)} className={cls} {...extra}>
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
