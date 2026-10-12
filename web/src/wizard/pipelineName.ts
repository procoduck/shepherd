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

/**
 * The key the server's uniqueness check compares: migration 0002's
 * sanitize_pipeline_name, mirroring internal/merge.SanitizeName — lowercase,
 * every character outside [a-z0-9_] becomes '_', and a leading digit gets a
 * 'p'. "Pod Logs", "pod_logs" and "pod-logs" are therefore one name.
 */
export function sanitizePipelineName(name: string): string {
  const s = name.toLowerCase().replace(/[^a-z0-9_]/g, '_');
  return /^[0-9]/.test(s) ? `p${s}` : s;
}

/** The org's names as the server compares them. */
export function takenNameKeys(names: Iterable<string>): Set<string> {
  return new Set([...names].map(sanitizePipelineName));
}

/** Whether `name` collides with one of `takenKeys` (from takenNameKeys). */
export function isNameTaken(name: string, takenKeys: Set<string>): boolean {
  return takenKeys.has(sanitizePipelineName(name));
}

/** The org's existing name that `name` collides with, as that pipeline is
 *  named ("Pod Logs" for "pod-logs"), or undefined when none does. */
export function collidingName(name: string, names: Iterable<string>): string | undefined {
  const key = sanitizePipelineName(name);
  for (const existing of names) {
    if (sanitizePipelineName(existing) === key) return existing;
  }
  return undefined;
}

/** `name` itself when no pipeline has it, else the first free `name-N` (N ≥ 2).
 *  "Has it" is the server's sanitized comparison, not string equality. */
export function uniquePipelineName(name: string, taken: Iterable<string>): string {
  const keys = takenNameKeys(taken);
  if (!isNameTaken(name, keys)) return name;
  for (let n = 2; ; n++) {
    const candidate = `${name}-${n}`;
    if (!isNameTaken(candidate, keys)) return candidate;
  }
}
