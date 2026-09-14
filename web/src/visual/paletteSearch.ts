/**
 * Ranks palette search results (F12: `Palette.tsx` used to `.filter(...includes...)`
 * and kept whatever order the schema's `components` object happened to
 * enumerate in — so a component that only matched by a substring of its doc
 * text could out-rank the component whose *name* the query names exactly).
 *
 * Pure and schema-agnostic (only `name`/`doc` are read) so it is testable
 * without a `SchemaPayload` fixture and reusable anywhere a searchable list
 * of named, documented items needs ranking.
 *
 * Tiers, best first: exact name match; a `.`/`_`-delimited segment of the
 * name equals the query (`remote_write` for `prometheus.remote_write`);
 * name starts with the query; name contains the query; doc text contains
 * the query. Items that match no tier are dropped. Ties within a tier sort
 * alphabetically by name, so the ranking is stable and deterministic.
 */
export interface PaletteSearchItem {
  name: string;
  doc?: string;
}

const TIER_EXACT = 0;
const TIER_SEGMENT = 1;
const TIER_PREFIX = 2;
const TIER_SUBSTRING = 3;
const TIER_DOC = 4;

function nameSegments(name: string): string[] {
  return name
    .toLowerCase()
    .split(/[^a-z0-9]+/i)
    .filter(Boolean);
}

function tierFor(query: string, item: PaletteSearchItem): number | null {
  const name = item.name.toLowerCase();
  if (name === query) return TIER_EXACT;
  if (nameSegments(item.name).includes(query)) return TIER_SEGMENT;
  if (name.startsWith(query)) return TIER_PREFIX;
  if (name.includes(query)) return TIER_SUBSTRING;
  if (item.doc?.toLowerCase().includes(query)) return TIER_DOC;
  return null;
}

/**
 * Filters `items` to those matching `query` (case-insensitive, against name
 * or doc) and orders them best-match first. An empty/whitespace-only query
 * returns `items` unchanged — the palette's unfiltered listing.
 */
export function rankPaletteItems<T extends PaletteSearchItem>(query: string, items: T[]): T[] {
  const q = query.trim().toLowerCase();
  if (!q) return items;
  return items
    .map((item) => ({ item, tier: tierFor(q, item) }))
    .filter((scored): scored is { item: T; tier: number } => scored.tier !== null)
    .sort((a, b) => a.tier - b.tier || a.item.name.localeCompare(b.item.name))
    .map((scored) => scored.item);
}
