/**
 * Line-level +added/−removed counts for the "View diff" caption above
 * RevisionDiff — not the diff itself (that's @codemirror/merge's job), just
 * a cheap summary a caller can render before the CodeMirror chunk has
 * loaded.
 *
 * This is a line-multiset comparison, not an LCS/alignment diff: a line
 * present N times in one text and M times in the other counts
 * max(N-M, 0) removed / max(M-N, 0) added, with no attempt to pair a
 * specific old line with a specific new one (a same-count reorder shows as
 * 0/0, and a line edited in place shows as one add + one remove — a
 * "replacement" — rather than a modify). That is the right shape for a
 * one-line caption; a precise line-pairing diff is what MergeView already
 * renders in the pane below it.
 */
export function diffStats(oldText: string, newText: string): { added: number; removed: number } {
  const oldCounts = lineCounts(oldText);
  const newCounts = lineCounts(newText);

  let added = 0;
  let removed = 0;
  const allLines = new Set([...oldCounts.keys(), ...newCounts.keys()]);
  for (const line of allLines) {
    const before = oldCounts.get(line) ?? 0;
    const after = newCounts.get(line) ?? 0;
    if (after > before) added += after - before;
    else if (before > after) removed += before - after;
  }
  return { added, removed };
}

function lineCounts(text: string): Map<string, number> {
  const counts = new Map<string, number>();
  // A trailing newline would otherwise contribute a spurious empty final
  // "line" that both texts always share (net zero) — split() alone doesn't
  // strip it, so drop one trailing blank entry if the text ended in \n.
  const lines = text.split('\n');
  if (lines.length > 0 && lines[lines.length - 1] === '' && text.endsWith('\n')) {
    lines.pop();
  }
  for (const line of lines) {
    counts.set(line, (counts.get(line) ?? 0) + 1);
  }
  return counts;
}
