import { Link } from '@tanstack/react-router';
import type { ReactNode } from 'react';

/** A pipeline the message may name: only id and name are read. */
export interface PipelineRef {
  id: string;
  name: string;
}

// A Go %q string: what the server quotes every pipeline name with in a
// destination refusal (internal/mgmtapi destination_rerender.go and
// rpc_destination.go).
const QUOTED = /"(?:[^"\\]|\\.)*"/g;

/**
 * `message` with every quoted pipeline name turned into a link to that
 * pipeline's page (M4, 2026-10-08 walkthrough). A destination refusal names
 * the wizard pipelines it is about and tells the person to act on each one
 * — restore, detach or delete, all on the pipeline's page — so each name is
 * one click from where that happens.
 *
 * Only a quoted name of a pipeline in `pipelines` becomes a link, and never
 * one that follows "destination " — a refusal quotes the destination's
 * own name too, and a pipeline may share it. Anything that does not parse
 * as a quoted string stays text, so the worst case is the message as the
 * server wrote it.
 */
export function linkPipelineNames(message: string, pipelines: readonly PipelineRef[]): ReactNode {
  if (pipelines.length === 0) return message;
  const byName = new Map(pipelines.map((p) => [p.name, p.id]));
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of message.matchAll(QUOTED)) {
    const start = m.index ?? 0;
    // Case-insensitive: formError raises the refusal's first letter.
    if (/destination $/i.test(message.slice(0, start))) continue;
    let name: string;
    try {
      name = JSON.parse(m[0]) as string;
    } catch {
      continue;
    }
    const id = byName.get(name);
    if (!id) continue;
    out.push(message.slice(last, start));
    out.push(
      <span key={start}>
        &ldquo;
        <Link
          to='/pipelines/$id'
          params={{ id }}
          className='font-medium underline underline-offset-2 hover:opacity-80'
        >
          {name}
        </Link>
        &rdquo;
      </span>,
    );
    last = start + m[0].length;
  }
  if (out.length === 0) return message;
  out.push(message.slice(last));
  return out;
}
