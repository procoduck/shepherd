import { Boxes } from 'lucide-react';
import { useEffect, useMemo, useRef, useState } from 'react';
import { canConnectPorts, resolvePorts } from '../l1';
import { useVisualStore } from '../store';
import { CollapsiblePanel } from './CollapsiblePanel';

const CATEGORIES = ['sources', 'transform', 'destinations', 'config', 'advanced'] as const;
const LABELS: Record<string, string> = {
  sources: 'Sources',
  transform: 'Transform',
  destinations: 'Destinations',
  config: 'Config',
  advanced: 'Advanced',
};
// Mirrors the border color families PipelineNode/CATEGORY_BORDER use, as a plain
// bg-* dot instead of a border-l-* class. Kept local (not schemaAdapter.ts) since
// A4 replaces CATEGORY_BORDER's source with schema-driven colors separately.
const CATEGORY_DOT: Record<string, string> = {
  sources: 'bg-blue-500',
  transform: 'bg-purple-500',
  destinations: 'bg-emerald-500',
  config: 'bg-slate-400',
  advanced: 'bg-slate-300',
};

export function Palette() {
  const schema = useVisualStore((s) => s.schema);
  const selected = useVisualStore((s) => s.selected);
  const doc = useVisualStore((s) => s.doc);
  const addNode = useVisualStore((s) => s.addNode);
  const allowExperimental = useVisualStore((s) => s.allowExperimental);
  const [search, setSearch] = useState('');
  const [showAllOverride, setShowAllOverride] = useState(false);
  const clickCountRef = useRef(0);

  const selectedNode =
    selected.length === 1 ? doc.nodes.find((n) => n.id === selected[0]) : undefined;
  const selectedDef = selectedNode && schema?.components[selectedNode.component];
  const filterBySelected = !!selectedDef && !showAllOverride && !search;

  useEffect(() => {
    setShowAllOverride(false);
  }, [selected]);

  const items = useMemo(() => {
    if (!schema) return [];
    return Object.entries(schema.components)
      .filter(([, def]) => allowExperimental || def.stability !== 'experimental')
      .map(([name, def]) => ({ name, def, category: def.category ?? 'advanced' }));
  }, [schema, allowExperimental]);

  const filtered = useMemo(() => {
    let base = items;
    if (search) {
      const q = search.toLowerCase();
      base = base.filter(
        (c) => c.name.toLowerCase().includes(q) || c.def.doc?.toLowerCase().includes(q),
      );
    } else if (filterBySelected && selectedDef) {
      // D1: compatibility is a ROLE match (one port produces, the other
      // accepts), not a raw input/output match — a receiver-kind export
      // (e.g. prometheus.remote_write.receiver) accepts, and the argument
      // that references it (forward_to) produces, so the old "selected's
      // outputs vs candidate's inputs" check missed exactly the wires this
      // canvas exists to draw (every destination). Reuses l1.ts's role
      // resolution so the palette can't drift from what the canvas will
      // actually let the user connect.
      const selectedPorts = resolvePorts(selectedDef);
      base = base.filter((c) => {
        const candidatePorts = resolvePorts(c.def);
        return selectedPorts.some((sp) =>
          candidatePorts.some((cp) => canConnectPorts(sp, cp) || canConnectPorts(cp, sp)),
        );
      });
    }
    return base;
  }, [items, search, filterBySelected, selectedDef]);

  // Grid the stagger so successive click-placed nodes never overlap (task
  // item 7): a PipelineNode is 240 flow-px wide (`w-60`), and since zoom
  // scales flow-space uniformly, an offset smaller than that overlaps at
  // EVERY zoom level, not just the maxZoom=2 the initial fitView clamps to
  // (the review measured the old 60px x-offset doing exactly that: 480px-wide
  // rendered boxes 60*2=120px apart). 320/200 clear the widest node plus a
  // margin, and multi-tall nodes (several ports) plus margin respectively.
  const PALETTE_GRID_COLS = 4;
  const PALETTE_COL_SPACING = 320;
  const PALETTE_ROW_SPACING = 200;
  const handleClick = (name: string) => {
    const count = clickCountRef.current;
    clickCountRef.current += 1;
    const col = count % PALETTE_GRID_COLS;
    const row = Math.floor(count / PALETTE_GRID_COLS);
    const placement = useVisualStore.getState().getPlacement;
    const position = placement
      ? placement(count)
      : { x: 80 + col * PALETTE_COL_SPACING, y: 80 + row * PALETTE_ROW_SPACING };
    addNode(name, position, { refitView: true });
    setSearch('');
  };

  return (
    <CollapsiblePanel
      side='left'
      storageKey='vb.palette.collapsed'
      title='Components'
      width='w-56'
      testId='palette'
      collapsedIcon={<Boxes size={16} />}
    >
      <div
        className='flex flex-col min-h-0 flex-1 overflow-hidden'
        onWheel={(e) => e.stopPropagation()}
      >
        <div className='p-2 border-b border-border'>
          <input
            type='text'
            placeholder='Search components...'
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className='w-full px-2 py-1.5 text-sm rounded-md border border-border-strong bg-panel placeholder:text-muted-2 outline-none focus:border-accent'
            aria-label='Search palette'
            data-testid='palette-search'
          />
        </div>
        {filterBySelected && selectedNode && (
          <div className='px-3 py-1.5 bg-indigo-950/50 border-b border-border flex items-center justify-between gap-2 text-xs'>
            <span className='text-indigo-300 truncate'>
              Compatible with <span className='font-mono'>{selectedNode.component}</span>
            </span>
            <button
              onClick={() => setShowAllOverride(true)}
              className='text-indigo-400 hover:text-white shrink-0'
              aria-label='Show all components'
            >
              ✕
            </button>
          </div>
        )}
        <div className='flex-1 overflow-y-auto'>
          {CATEGORIES.map((cat) => {
            const catItems = filtered.filter((c) => c.category === cat);
            if (catItems.length === 0) return null;
            return (
              <details key={cat} open className='border-b border-border'>
                <summary className='px-3 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-2 cursor-pointer select-none'>
                  {LABELS[cat]}
                </summary>
                {catItems.map(({ name, def }) => (
                  <div
                    key={name}
                    draggable
                    data-component={name}
                    data-testid={`palette-item-${name}`}
                    onDragStart={(e) => e.dataTransfer.setData('application/vb-component', name)}
                    onClick={() => handleClick(name)}
                    className='px-3 py-1.5 text-sm cursor-pointer hover:bg-accent/10 flex items-center gap-2'
                  >
                    <span
                      className={`h-1.5 w-1.5 rounded-full shrink-0 ${CATEGORY_DOT[cat]}`}
                      aria-hidden='true'
                    />
                    <span className='font-mono truncate flex-1'>{name}</span>
                    {def.stability !== 'ga' && (
                      <span
                        className={`text-xs px-1 rounded shrink-0 ${
                          def.stability === 'experimental'
                            ? 'bg-amber-200 text-amber-800'
                            : 'bg-sky-200 text-sky-800'
                        }`}
                      >
                        {def.stability === 'experimental' ? 'exp' : 'preview'}
                      </span>
                    )}
                  </div>
                ))}
              </details>
            );
          })}
        </div>
      </div>
    </CollapsiblePanel>
  );
}
