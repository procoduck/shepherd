/**
 * The schema payload used by the frontend tests.
 *
 * It is DERIVED, never authored. Every component definition below is sliced
 * verbatim out of the artifact the Go server embeds
 * (`internal/schema/artifacts/alloy-v<version>.json`), deep-merged with the same
 * `overlay.json` the server merges, using the same merge semantics as
 * `internal/schema/registry.go`.
 *
 * This file used to hand-write nine components, and its port model was the
 * mirror image of the shipped one — `prometheus.scrape` "exported" metrics and
 * `prometheus.remote_write` "accepted" a receiver. Thirteen files imported it,
 * including every Playwright visual spec, so the whole canvas was proved against
 * a component model no server has ever returned. Nothing here may be written by
 * hand again: change the pinned subset if a test needs another component, never
 * the shape of a component.
 */
import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import type { ComponentDef, SchemaPayload } from '@/visual/types';

// This module is loaded by two runners: Vitest (which transpiles it) and
// Playwright (which loads it as ESM, since web/package.json is "type": "module"
// — there is no __dirname there). import.meta.url is the one form both provide.
const moduleDir = dirname(fileURLToPath(import.meta.url));
const artifactsDir = join(moduleDir, '../../../internal/schema/artifacts');
const readJson = (file: string): unknown =>
  JSON.parse(readFileSync(join(artifactsDir, file), 'utf-8'));

const isObj = (v: unknown): v is Record<string, unknown> =>
  typeof v === 'object' && v !== null && !Array.isArray(v);

/** The server's deepMerge (internal/schema/registry.go): overlay over artifact. */
function deepMerge(base: unknown, over: unknown): unknown {
  if (!isObj(base) || !isObj(over)) return over;
  const out: Record<string, unknown> = { ...base };
  for (const [k, v] of Object.entries(over)) out[k] = k in base ? deepMerge(base[k], v) : v;
  return out;
}

/** The single committed artifact; picked by pattern so an Alloy bump needs no edit here. */
const artifactFile = readdirSync(artifactsDir)
  .filter((f) => f.startsWith('alloy-') && f.endsWith('.json'))
  .sort()
  .at(-1);
if (!artifactFile) throw new Error(`no alloy-*.json artifact in ${artifactsDir}`);

/** Exactly what GET /api/schema/current serves: artifact + overlay, all 184 components. */
export const shippedSchema = deepMerge(
  readJson(artifactFile),
  readJson('overlay.json'),
) as SchemaPayload;

/**
 * The components the visual specs place on the canvas. Curating WHICH components
 * appear is a test-speed decision and is fine to write by hand; curating what
 * they LOOK like is not, which is why only names live here.
 *
 * `loki.secretfilter` is the experimental entry: the palette's stability gate and
 * L1's `experimental_gated` rule need one, and it must be a component that really
 * carries `stability: "experimental"` in the artifact.
 */
export const FIXTURE_COMPONENTS = [
  'discovery.kubernetes',
  'discovery.relabel',
  'prometheus.scrape',
  'prometheus.remote_write',
  'prometheus.relabel',
  'loki.source.file',
  'loki.process',
  'loki.write',
  'loki.secretfilter',
] as const;

const components: Record<string, ComponentDef> = {};
for (const name of FIXTURE_COMPONENTS) {
  const def = (shippedSchema.components as Record<string, ComponentDef | undefined>)[name];
  if (!def) throw new Error(`${artifactFile} declares no component ${name}`);
  components[name] = def;
}

export const schemaFixture: SchemaPayload = {
  ...shippedSchema,
  _meta: { ...shippedSchema._meta, components_total: FIXTURE_COMPONENTS.length },
  components,
};
