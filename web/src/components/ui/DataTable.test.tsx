/**
 * @vitest-environment jsdom
 */
import { cleanup, fireEvent, render, screen } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { DataTable } from './DataTable';

// No `test.globals`, so testing-library's automatic cleanup never registers.
afterEach(() => {
  cleanup();
  window.getSelection()?.removeAllRanges();
});

type Row = { id: string; name: string };

function renderTable(onRowClick: (row: Row) => void) {
  render(
    <DataTable<Row>
      columns={[
        { key: 'name', header: 'Name', render: (r) => <a href={`#${r.id}`}>{r.name}</a> },
        { key: 'plain', header: 'Plain', render: (r) => <span>plain {r.id}</span> },
        {
          key: 'act',
          header: 'Act',
          render: (r) => (
            <button type='button'>
              <span>toggle {r.id}</span>
            </button>
          ),
        },
      ]}
      rows={[{ id: 'a', name: 'alpha' }]}
      rowKey={(r) => r.id}
      onRowClick={onRowClick}
    />,
  );
}

describe('DataTable onRowClick', () => {
  it('fires for a plain click on a cell', () => {
    const onRowClick = vi.fn();
    renderTable(onRowClick);
    fireEvent.click(screen.getByText('plain a'));
    expect(onRowClick).toHaveBeenCalledWith({ id: 'a', name: 'alpha' });
  });

  it('leaves clicks on inner links and buttons (and their children) to them', () => {
    const onRowClick = vi.fn();
    renderTable(onRowClick);
    fireEvent.click(screen.getByText('alpha'));
    fireEvent.click(screen.getByText('toggle a'));
    expect(onRowClick).not.toHaveBeenCalled();
  });

  it('ignores a click that ends a text selection', () => {
    const onRowClick = vi.fn();
    renderTable(onRowClick);
    const text = screen.getByText('plain a');
    const range = document.createRange();
    range.selectNodeContents(text);
    window.getSelection()?.addRange(range);
    fireEvent.click(text);
    expect(onRowClick).not.toHaveBeenCalled();
  });

  it('ignores modified and non-primary clicks', () => {
    const onRowClick = vi.fn();
    renderTable(onRowClick);
    const text = screen.getByText('plain a');
    fireEvent.click(text, { metaKey: true });
    fireEvent.click(text, { ctrlKey: true });
    fireEvent.click(text, { button: 1 });
    expect(onRowClick).not.toHaveBeenCalled();
  });

  it('does not make the row focusable', () => {
    renderTable(vi.fn());
    const row = screen.getByText('plain a').closest('tr');
    expect(row?.hasAttribute('tabindex')).toBe(false);
  });
});
