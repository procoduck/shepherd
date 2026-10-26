import type { Timestamp } from '@bufbuild/protobuf/wkt';
import { toneClass } from '@/components/ui/statusTone';
import { formatTimestampRelative } from '@/lib/utils';

// #375: what the agent API's version check of a collector's served config
// found (Collector.version_warning, migration 0044). Stage 3 refuses a save
// that uses an item missing from an active instance's Alloy version; when the
// instances change afterwards (an older pod joins) the config keeps being
// served and this is where the mismatch shows. A mismatch means an instance
// will most likely reject the config and keep its last one; otherwise every
// finding is a note (a version checked against the pinned schema only, a
// component that needs --stability.level).

interface VersionWarning {
  findings: string[];
  mismatch: boolean;
  since?: Timestamp;
}

interface Warnable {
  versionWarning?: VersionWarning;
}

function headline(w: VersionWarning): string {
  return w.mismatch
    ? "Alloy version mismatch — an instance's Alloy version lacks part of its config"
    : 'Alloy version notes';
}

/** The Collectors list's marker beside the version; renders nothing without a warning. */
export function VersionWarningBadge({ collector }: { collector: Warnable }) {
  const w = collector.versionWarning;
  if (!w || w.findings.length === 0) return null;
  return (
    <span
      data-testid='collector-version-warning-badge'
      title={`${headline(w)}:\n${w.findings.join('\n')}`}
      className={`ml-2 text-xs font-medium px-1.5 py-0.5 rounded border ${toneClass(w.mismatch ? 'warn' : 'info')}`}
    >
      {w.mismatch ? 'MISMATCH' : 'NOTE'}
    </span>
  );
}

/** The collector page's list of findings; renders nothing without a warning. */
export function CollectorVersionWarningBanner({ collector }: { collector: Warnable }) {
  const w = collector.versionWarning;
  if (!w || w.findings.length === 0) return null;
  return (
    <div
      className={`rounded-lg border px-4 py-3 text-sm ${toneClass(w.mismatch ? 'warn' : 'info')}`}
      data-testid='collector-version-warning'
    >
      <p className='font-medium'>{headline(w)}</p>
      <p className='mt-1'>
        {w.mismatch
          ? 'Shepherd keeps serving the validated config. Upgrade the older instances, or change the pipeline so every active version can load it.'
          : 'Nothing here stops the config loading on a collector run with the flags named below.'}{' '}
        Since {formatTimestampRelative(w.since)}.
      </p>
      <ul className='mt-2 list-disc pl-5 font-mono text-xs break-words'>
        {w.findings.map((f) => (
          <li key={f}>{f}</li>
        ))}
      </ul>
    </div>
  );
}
