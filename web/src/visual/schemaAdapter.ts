import { currentTheme, type Theme } from '../theme';
import type { ComponentDef, SchemaPayload, WireTypeDef } from './types';
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
// The overlay serves `wire_types.<id>.color` (already in SchemaPayload) and a
// `categories.<id>.color` section (backend half of A4 — not yet in `types.ts`,
// which this task doesn't own, so we read it defensively via a local optional
// type instead of widening SchemaPayload). getWireColor/getCategoryColor are
// the ONE place color resolution happens: schema value first, a single
// neutral default second. The three hand-maintained hex fallback tables that
// used to live here (and, before that, duplicated in CanvasPane.tsx as
// WIRE_COLOR/CATEGORY_BORDER/FLOW_COLORS) drifted from each other and from
// the overlay itself, so they're gone — the overlay now defines every wire
// type and category the shipped schema serves; DEFAULT_WIRE_COLOR and
// DEFAULT_CATEGORY_COLOR only matter for a payload that's missing one (e.g.
// a cached schema version predating a new entry).
export const DEFAULT_WIRE_COLOR = '#94a3b8';
export const DEFAULT_CATEGORY_COLOR = '#64748b';

/** SchemaPayload widened with the categories section the backend half of A4
 * adds to the overlay. Declared locally rather than in types.ts (out of scope
 * for this task) so this module works whether or not that field exists yet. */
type SchemaWithCategories = SchemaPayload & {
  categories?: Record<string, { color?: string; label?: string } | undefined>;
};

/** Resolves a wire type's display color: schema payload first, hex fallback second. */
export function getWireColor(schema: SchemaPayload | null | undefined, wireType: string): string {
  return schema?.wire_types[wireType]?.color || DEFAULT_WIRE_COLOR;
}

/** Resolves a component category's display color: schema payload first, hex fallback second. */
export function getCategoryColor(
  schema: SchemaPayload | null | undefined,
  category: string,
): string {
  const withCategories = schema as SchemaWithCategories | null | undefined;
  return withCategories?.categories?.[category]?.color || DEFAULT_CATEGORY_COLOR;
}

// --- Theme-aware colors (D8, light mode — W6-S1) ---
//
// ADDITIVE ONLY: getWireColor/getCategoryColor above are unchanged and still
// the dark-mode resolution every current caller (CanvasPane, etc.) uses.
// getThemedWireColor/getThemedCategoryColor are new exports nothing calls
// yet — wave 2 adopts them once the canvas surface itself is theme-aware.
// They prefer the overlay's `color_light` in the light theme (see
// internal/schema/artifacts/overlay.json and schema_test.go's "every wire
// type/category also carries a hex color_light" guards), falling back to
// the existing dark resolution when the payload has none yet (e.g. a cached
// schema version predating this field).

/** Wire type widened with the `color_light` field D8 adds to the overlay.
 * Declared locally rather than in types.ts (out of this task's territory),
 * same reasoning as SchemaWithCategories above. */
type WireTypeDefWithLight = WireTypeDef & { color_light?: string };
type SchemaWithLightWires = SchemaPayload & {
  wire_types: Record<string, WireTypeDefWithLight | undefined>;
};

/** Resolves a wire type's display color for the given (or current) theme. */
export function getThemedWireColor(
  schema: SchemaPayload | null | undefined,
  wireType: string,
  theme: Theme = currentTheme(),
): string {
  if (theme === 'light') {
    const withLightWires = schema as SchemaWithLightWires | null | undefined;
    const light = withLightWires?.wire_types[wireType]?.color_light;
    if (light) return light;
  }
  return getWireColor(schema, wireType);
}

/** Category widened with the `color_light` field D8 adds to the overlay. */
type SchemaWithLightCategories = SchemaWithCategories & {
  categories?: Record<string, { color?: string; color_light?: string; label?: string } | undefined>;
};

/** Resolves a component category's display color for the given (or current) theme. */
export function getThemedCategoryColor(
  schema: SchemaPayload | null | undefined,
  category: string,
  theme: Theme = currentTheme(),
): string {
  if (theme === 'light') {
    const withLightCategories = schema as SchemaWithLightCategories | null | undefined;
    const light = withLightCategories?.categories?.[category]?.color_light;
    if (light) return light;
  }
  return getCategoryColor(schema, category);
}
