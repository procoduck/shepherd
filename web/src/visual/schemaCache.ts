/**
 * The served component schema (`GET /api/schema/current`), fetched once and
 * shared (#251).
 *
 * The payload is the largest thing the builder loads — about 1.4 MB of JSON
 * for Alloy v1.20 — and it used to be fetched only from the builder's mount
 * effect: after the route's chunks had downloaded, been evaluated and
 * rendered once. Every visit fetched and parsed it again, and `setSchema`
 * then re-validated the whole graph against it.
 *
 * Now the route starts the fetch as it is entered (router.tsx, before the
 * lazy builder chunk has even loaded), the builder renders at once from the
 * copy it already holds on a later visit, and a revalidation whose ETag is
 * unchanged is not parsed again. `current` is a moving pointer (a server
 * upgrade changes it — see internal/mgmtapi/schema.go), so a held copy is
 * always revalidated rather than trusted for the session.
 *
 * Deliberately free of React and the rest of the builder: router.tsx imports
 * it into the entry chunk.
 */
import type { SchemaPayload } from './types';

interface Entry {
  etag: string | null;
  schema: SchemaPayload;
  /** `Date.now()` when this copy was fetched or last confirmed unchanged. */
  at: number;
}

/** A fetch younger than this counts as fresh — no revalidation. Covers the
 *  gap between the route starting the fetch and the builder mounting. */
const FRESH_MS = 10_000;

let latest: Entry | null = null;
let inflight: Promise<Entry> | null = null;

async function fetchCurrent(): Promise<Entry> {
  const res = await fetch('/api/schema/current', {
    headers: { 'X-Requested-With': 'XMLHttpRequest' },
  });
  if (!res.ok) throw new Error(`Failed to load schema: ${res.status}`);
  const etag = res.headers.get('ETag');
  // Unchanged since the copy already held (the browser revalidated with the
  // ETag and got a 304): keep that copy, skip re-parsing the payload.
  if (etag && latest && latest.etag === etag) {
    void res.body?.cancel().catch(() => undefined);
    return { ...latest, at: Date.now() };
  }
  return { etag, schema: (await res.json()) as SchemaPayload, at: Date.now() };
}

/** Starts fetching the current schema, unless a fetch is already in flight;
 *  resolves to the payload. Safe to call on every navigation. */
export function prefetchCurrentSchema(): Promise<SchemaPayload> {
  if (!inflight) {
    const p = fetchCurrent().then((entry) => {
      latest = entry;
      return entry;
    });
    inflight = p;
    const clear = () => {
      if (inflight === p) inflight = null;
    };
    p.then(clear, clear);
  }
  return inflight.then((e) => e.schema);
}

/** The copy already held, if any — for rendering at once while a fresh one
 *  is confirmed. */
export function cachedCurrentSchema(): SchemaPayload | null {
  return latest?.schema ?? null;
}

/** The current schema: the in-flight fetch if there is one, the held copy if
 *  it is fresh, otherwise a new fetch. A payload whose ETag did not change
 *  resolves to the very same object as `cachedCurrentSchema()`, so a caller
 *  can tell "nothing new" by identity. */
export function loadCurrentSchema(): Promise<SchemaPayload> {
  if (inflight) return inflight.then((e) => e.schema);
  if (latest && Date.now() - latest.at < FRESH_MS) return Promise.resolve(latest.schema);
  return prefetchCurrentSchema();
}

/** Test seam: forget everything held. */
export function resetSchemaCacheForTests(): void {
  latest = null;
  inflight = null;
}
