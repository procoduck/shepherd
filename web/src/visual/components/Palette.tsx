import { useEffect, useMemo, useRef, useState } from 'react';
import { portsCompatible } from '../l1';
import { useVisualStore } from '../store';

const CATEGORIES = ['sources', 'transform', 'destinations', 'config', 'advanced'] as const;
const LABELS: Record<string, string> = {
  sources: 'Sources',
  transform: 'Transform',
  destinations: 'Destinations',
  config: 'Config',
  advanced: 'Advanced',
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
      const selectedOutputTypes = selectedDef.outputs.map((o) => o.type);
      const selectedInputTypes = selectedDef.inputs.map((i) => i.type);
      base = base.filter((c) => {
        const canReceive = c.def.inputs.some((inp) =>
          selectedOutputTypes.some((ot) => portsCompatible(ot, inp.type)),
        );
        const canFeed = c.def.outputs.some((out) =>
          selectedInputTypes.some((it) => portsCompatible(out.type, it)),
        );
        return canReceive || canFeed;
      });
    }
    return base;
  }, [items, search, filterBySelected, selectedDef]);

  const handleClick = (name: string) => {
    // Stagger each successive click-placed node so they don't overlap.
    const count = clickCountRef.current;
    clickCountRef.current += 1;
    const offset = count * 60;
    addNode(name, { x: 80 + offset, y: 80 + (count % 3) * 80 });
    setSearch('');
  };

  return (
    <div
      className='w-64 border-r flex flex-col overflow-hidden shrink-0'
      data-testid='palette'
      onWheel={(e) => e.stopPropagation()}
    >
      <div className='p-2 border-b'>
        <input
          type='text'
          placeholder='Search components...'
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className='w-full px-2 py-1 text-sm border rounded bg-background'
          aria-label='Search palette'
          data-testid='palette-search'
        />
      </div>
      {filterBySelected && selectedNode && (
        <div className='px-3 py-1.5 bg-indigo-950/50 border-b flex items-center justify-between gap-2 text-xs'>
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
            <details key={cat} open className='border-b'>
              <summary className='px-3 py-1.5 text-xs font-semibold uppercase tracking-wide cursor-pointer select-none'>
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
                  className='px-3 py-1.5 text-sm cursor-pointer hover:bg-accent flex items-center justify-between gap-1'
                >
                  <span className='font-mono truncate'>{name}</span>
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
  );
}
