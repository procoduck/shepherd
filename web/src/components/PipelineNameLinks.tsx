import { toJson } from '@bufbuild/protobuf';
import { StructSchema } from '@bufbuild/protobuf/wkt';
import { ConnectError } from '@connectrpc/connect';
import { Link } from '@tanstack/react-router';
import type { ReactNode } from 'react';

/** A pipeline a refusal names. */
export interface PipelineRef {
  id: string;
  name: string;
}

/**
 * The pipelines a destination refusal is about (M4), read from its Connect
 * error detail — a google.protobuf.Struct `{"pipelines": [{id, name}]}` the
 * server attaches (internal/mgmtapi pipelines_detail.go). Never parsed out
 * of the message: it also quotes the destination's name and Alloy labels
 * from validation diagnostics, any of which can equal a pipeline's name.
 * Empty when the error carries no such detail.
 */
export function refusedPipelines(e: unknown): PipelineRef[] {
  if (!e) return [];
  const refs: PipelineRef[] = [];
  for (const st of ConnectError.from(e).findDetails(StructSchema)) {
    const json = toJson(StructSchema, st) as { pipelines?: unknown };
    if (!Array.isArray(json.pipelines)) continue;
    for (const p of json.pipelines) {
      const { id, name } = (p ?? {}) as { id?: unknown; name?: unknown };
      if (typeof id === 'string' && id && typeof name === 'string') refs.push({ id, name });
    }
  }
  return refs;
}

/**
 * `message` followed by a link to each pipeline the refusal names, so the
 * person is one click from where they can restore, detach or delete it.
 */
export function withPipelineLinks(message: string | null, e: unknown): ReactNode {
  if (!message) return null;
  const refs = refusedPipelines(e);
  if (refs.length === 0) return message;
  return (
    <>
      {message}
      <span className='mt-1.5 block' data-testid='refused-pipelines'>
        Open {refs.length === 1 ? 'the pipeline' : 'each pipeline'}:{' '}
        {refs.map((r, i) => (
          <span key={r.id}>
            {i > 0 && ', '}
            <Link
              to='/pipelines/$id'
              params={{ id: r.id }}
              className='font-medium underline underline-offset-2 hover:opacity-80'
            >
              {r.name}
            </Link>
          </span>
        ))}
      </span>
    </>
  );
}
