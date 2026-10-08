import { create } from '@bufbuild/protobuf';
import { describe, expect, it } from 'vitest';
import { MatchedCollectorSchema } from '@/gen/shepherd/mgmt/v1/pipeline_pb';
import { exclusionLines } from './PipelineMatchPreview';

const mc = (cluster: string, role: string, excludedReason = '') =>
  create(MatchedCollectorSchema, { cluster, role, id: `${cluster}-${role}`, excludedReason });

describe('exclusionLines', () => {
  it('is empty when no matched collector is excluded', () => {
    expect(exclusionLines([mc('prod', 'logs'), mc('prod', 'metrics')])).toEqual([]);
  });

  it('groups by reason and counts collectors sharing a cluster/role', () => {
    const why = 'its signals (logs) are not allowed on role metrics';
    expect(
      exclusionLines([
        mc('prod', 'metrics', why),
        mc('prod', 'logs'),
        mc('dev', 'metrics', why),
        mc('prod', 'metrics', why),
        mc('prod', 'metrics', why),
      ]),
    ).toEqual([`Excluded from 4 collector(s): prod/metrics ×3, dev/metrics — ${why}.`]);
  });
});
