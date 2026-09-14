import type { SchemaPayload } from './types';

/**
 * The served schema's version in the form a GraphDocument records it
 * (`alloy-v1.19.2`). `_meta.alloy_version` has been written as `1.19.2`,
 * `v1.19.2` and `alloy-v1.19.2` over time; the document format is the
 * normalised one, so every comparison goes through here. Null when no schema
 * has loaded yet.
 *
 * This is the ONLY place the current version reaches the client: a document
 * must never be stamped with a literal, or every fleet bump turns each new
 * pipeline into one that "needs upgrading" against itself.
 */
export function currentSchemaVersion(schema: SchemaPayload | null | undefined): string | null {
  const raw = schema?._meta.alloy_version;
  if (!raw) return null;
  if (raw.startsWith('alloy-')) return raw;
  return `alloy-${raw.startsWith('v') ? '' : 'v'}${raw}`;
}
