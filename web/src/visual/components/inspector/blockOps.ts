/**
 * Pure structural ops for nested-block editing (D2 / task item 2): a props
 * value's block key holds an object (single block) or an array of objects
 * (repeatable block), and the inspector edits it by composing immutable
 * updates rather than writing at a JSON-pointer path. Kept free of React so
 * it's unit-testable without a DOM.
 */
import { isPlainObject } from './attributeOps';

export type BlockInstance = Record<string, unknown>;

/** Normalizes a props value at a block's key into its instances. Mirrors
 *  `l1.ts`'s `blockInstances` exactly (an object is one instance, an array is
 *  many, anything else — undefined, a stray scalar from a hand-edited graph —
 *  is none) so the inspector and the validator always agree on whether a
 *  block "has an instance" (D4: a malformed value degrades to "absent"
 *  rather than throwing). */
export function blockInstances(value: unknown): BlockInstance[] {
  if (value === undefined || value === null) return [];
  if (Array.isArray(value)) return value.filter(isPlainObject);
  if (isPlainObject(value)) return [value];
  return [];
}

/** Sets or deletes one attribute in an instance object. `undefined` deletes
 *  the key (an empty box is not a value — see attributeOps.coerceScalar). */
export function withAttr(instance: BlockInstance, name: string, value: unknown): BlockInstance {
  if (value === undefined) {
    if (!Object.hasOwn(instance, name)) return instance;
    const next = { ...instance };
    delete next[name];
    return next;
  }
  return { ...instance, [name]: value };
}

export function replaceAt<T>(list: T[], index: number, value: T): T[] {
  return list.map((v, i) => (i === index ? value : v));
}

export function removeAt<T>(list: T[], index: number): T[] {
  return list.filter((_, i) => i !== index);
}

export function appendItem<T>(list: T[], item: T): T[] {
  return [...list, item];
}

/**
 * Converts an instance array back into what `props[blockName]` should hold:
 * a single object for a non-repeatable block, the array itself for a
 * repeatable one, and `undefined` (delete the key) when there are no
 * instances left — an untouched or fully-removed block never appears in the
 * generated config (D2/D4), matching the renderer's own `blockInstances`
 * treatment of a missing key as zero instances.
 */
export function commitInstances(
  instances: BlockInstance[],
  repeatable: boolean,
): BlockInstance | BlockInstance[] | undefined {
  if (instances.length === 0) return undefined;
  return repeatable ? instances : instances[0];
}

/**
 * Maintains a node's `block_order` as blocks are added and removed.
 *
 * The graph stores blocks in `props`, an object keyed by block name, which
 * cannot hold the order the author arranged them in. Alloy block order is
 * semantic — loki.process runs stages in document order — so a node that
 * gained `stage.drop` after `stage.json` must remember that, or the renderer
 * falls back to the schema's own sequence and silently re-sequences the user's
 * pipeline.
 *
 * Appends on add (the new block goes last, where the user just put it),
 * removes on delete, and leaves the order untouched when an existing block is
 * merely edited. Attribute writes are not blocks and never touch it.
 */
export function nextBlockOrder(
  current: string[] | undefined,
  name: string,
  nextValue: unknown,
  isBlock: boolean,
): string[] | undefined {
  if (!isBlock) return current;
  const order = current ?? [];
  const removing = blockInstances(nextValue).length === 0;
  if (removing) {
    const out = order.filter((n) => n !== name);
    return out.length === order.length ? current : out;
  }
  if (order.includes(name)) return current;
  return [...order, name];
}
