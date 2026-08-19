/**
 * Local, structural shapes for schema fields the served payload carries that
 * `types.ts` (owned by the graph/schema tasks, not this one) does not declare
 * yet — `default` on an attribute, `required`/`path`/`role` on a block or
 * port. This is the same idiom `l1.ts` and `renderTS.ts` already use to read
 * the D1/D2 fields ahead of the shared type catching up: every field here is
 * optional, every reader below has a fallback, so a schema payload without
 * them still renders a usable (if less prefilled) form (D4).
 *
 * `ComponentDef`/`AttrDef`/`BlockDef` (from `../../types`) are structurally
 * assignable to these `*Like` shapes, so callers can pass the real typed
 * schema objects straight in without a cast.
 */

export interface AttrLike {
  name: string;
  type?: string;
  required?: boolean;
  values?: string[];
  input_type?: string;
  default?: unknown;
}

export interface BlockLike {
  name: string;
  required?: boolean;
  repeatable?: boolean;
  attributes?: AttrLike[];
  blocks?: BlockLike[];
}

export interface PortLike {
  prop?: string;
  export?: string;
  type?: string;
  cardinality?: string;
  role?: string;
  path?: string[];
}

export interface ComponentLike {
  doc?: string;
  stability?: string;
  category?: string;
  attributes?: AttrLike[];
  blocks?: BlockLike[];
  inputs?: PortLike[];
  outputs?: PortLike[];
}
