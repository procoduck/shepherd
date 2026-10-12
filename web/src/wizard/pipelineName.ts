/**
 * The wizard's default pipeline name (#306). Every wizard gets one — Pod Logs
 * and App Observability used to start blank, with Create disabled and no
 * reason — and it is made unique in the org, because the same wizard run
 * twice used to suggest the same name and the second commit was refused.
 */

function slugify(s: string): string {
  return s
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-+|-+$/g, '');
}

/** "<kind>-<job>", or just "<kind>" when there is no job name or the kind
 *  already ends with it (Blackbox's default job "blackbox" used to give
 *  "blackbox-blackbox"). */
export function suggestPipelineName(kind: string, jobName: string | undefined): string {
  const base = slugify(kind) || 'pipeline';
  const job = slugify(jobName ?? '');
  if (!job || base === job || base.endsWith(`-${job}`)) return base;
  return `${base}-${job}`;
}

/** `name` itself when no pipeline has it, else the first free `name-N` (N ≥ 2). */
export function uniquePipelineName(name: string, taken: Iterable<string>): string {
  const names = new Set(taken);
  if (!names.has(name)) return name;
  for (let n = 2; ; n++) {
    const candidate = `${name}-${n}`;
    if (!names.has(candidate)) return candidate;
  }
}
