import type { ComponentDef, SchemaPayload, WireTypeDef } from './types';
export async function fetchSchema(version = 'current'): Promise<SchemaPayload> {
  const res = await fetch(`/api/schema/${version}`, {
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  });
  if (!res.ok) throw new Error(`Failed to load schema: ${res.status}`);
  return res.json() as Promise<SchemaPayload>;
}
export function getComponent(schema: SchemaPayload, name: string): ComponentDef | undefined {
  return schema.components[name];
}
export function getCategory(schema: SchemaPayload, name: string): string {
  return schema.components[name]?.category ?? 'advanced';
}
export function getWireTypeDef(schema: SchemaPayload, type: string): WireTypeDef | undefined {
  return schema.wire_types[type];
}

/**
 * Resolves a port's React Flow handle id using ONE rule, used everywhere a
 * handle id or port lookup happens (PipelineNode's <Handle> ids, CanvasPane's
 * isValidConnection/onConnectStart/rfEdges, and l1's wire validation): named
 * ports keep their schema name (`prop`/`export`), unnamed ports fall back to a
 * stable positional id. Before this fix an unnamed port's <Handle id> (`String(i)`,
 * e.g. "0") never matched the id the validators looked up by (`p.export`/`p.prop`,
 * i.e. `undefined`), so the wire was silently rejected (A1 / R1-H1).
 */
export function portHandleId(port: { prop?: string; export?: string }, index: number): string {
  return port.prop ?? port.export ?? `p${index}`;
}

// --- Colors (A4) ---
//
// These hex tables are FALLBACKS only. The overlay is meant to serve
// `wire_types.<id>.color` (already in SchemaPayload) and a `categories.<id>.color`
// section (backend half of A4 — not yet in `types.ts`, which this task doesn't
// own, so we read it defensively via a local optional type instead of widening
// SchemaPayload). getWireColor/getCategoryColor are the ONE place color
// resolution happens now — they replace the three hand-maintained tables that
// used to live here and in CanvasPane.tsx (WIRE_COLOR, CATEGORY_BORDER,
// FLOW_COLORS), which drifted from each other (Tailwind classes vs hex,
// duplicated data). Once the backend always serves colors these fallbacks
// become dead code we can delete.
const WIRE_COLOR_FALLBACK: Record<string, string> = {
  targets: '#8b5cf6',
  'prom.metrics': '#f97316',
  'loki.logs': '#22c55e',
  'otel.traces': '#0ea5e9',
  'otel.metrics': '#0ea5e9',
  'otel.logs': '#0ea5e9',
  'otel.any': '#0ea5e9',
  'pyroscope.profiles': '#f43f5e',
};
const CATEGORY_COLOR_FALLBACK: Record<string, string> = {
  sources: '#3b82f6',
  transform: '#a855f7',
  destinations: '#10b981',
  config: '#94a3b8',
  advanced: '#cbd5e1',
};
const DEFAULT_WIRE_COLOR = '#94a3b8';
const DEFAULT_CATEGORY_COLOR = CATEGORY_COLOR_FALLBACK.advanced;

/** SchemaPayload widened with the categories section the backend half of A4
 * adds to the overlay. Declared locally rather than in types.ts (out of scope
 * for this task) so this module works whether or not that field exists yet. */
type SchemaWithCategories = SchemaPayload & {
  categories?: Record<string, { color?: string; label?: string } | undefined>;
};

/** Resolves a wire type's display color: schema payload first, hex fallback second. */
export function getWireColor(schema: SchemaPayload | null | undefined, wireType: string): string {
  return schema?.wire_types[wireType]?.color || WIRE_COLOR_FALLBACK[wireType] || DEFAULT_WIRE_COLOR;
}

/** Resolves a component category's display color: schema payload first, hex fallback second. */
export function getCategoryColor(
  schema: SchemaPayload | null | undefined,
  category: string,
): string {
  const withCategories = schema as SchemaWithCategories | null | undefined;
  return (
    withCategories?.categories?.[category]?.color ||
    CATEGORY_COLOR_FALLBACK[category] ||
    DEFAULT_CATEGORY_COLOR
  );
}
