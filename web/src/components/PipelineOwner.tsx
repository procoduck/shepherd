import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import { toast } from 'sonner';
import { clients, toApiError } from '@/api/transport';
import { Select } from '@/components/ui/Field';
import type { Pipeline } from '@/gen/shepherd/mgmt/v1/pipeline_pb';

/**
 * The pipeline's owning team (#252). ListTeams is org reader, so everyone who
 * can open the pipeline sees WHICH team owns it. Changing it goes through
 * PipelineService.SetPipelineOwner, which the server keeps org admin only
 * (rpc_interceptor.go) — ownership is a platform decision, not a delegated
 * one — so only `canAdminister` gets the picker. An empty choice clears the
 * owner.
 *
 * `queryOrgId` is the org the page's queries are keyed on; `orgId` is the
 * pipeline's own org, sent with the RPC.
 */
export function PipelineOwner({
  pipeline,
  orgId,
  queryOrgId,
  canAdminister,
}: {
  pipeline: Pipeline;
  orgId: string;
  queryOrgId: string;
  canAdminister: boolean;
}) {
  const qc = useQueryClient();
  const { data } = useQuery({
    queryKey: ['teams', queryOrgId],
    queryFn: () => clients.team.listTeams({ orgId: queryOrgId }),
    enabled: !!queryOrgId,
  });
  const teams = data?.items ?? [];
  const ownerTeam = teams.find((t) => t.id === pipeline.ownerTeamId);

  const setOwner = useMutation({
    mutationFn: (ownerTeamId: string) =>
      clients.pipeline.setPipelineOwner({ orgId, id: pipeline.id, ownerTeamId }),
    onSuccess: (p) => {
      const team = teams.find((t) => t.id === p.ownerTeamId);
      toast.success(team ? `Owner set to ${team.name}` : 'Owner cleared');
      const key = ['pipeline', queryOrgId, pipeline.id];
      qc.setQueryData(key, p);
      qc.invalidateQueries({ queryKey: key });
      qc.invalidateQueries({ queryKey: ['pipelines', queryOrgId] });
    },
    onError: (e) => toast.error(toApiError(e).message || 'Could not change the owner'),
  });

  if (!canAdminister) {
    return (
      <p>
        Owning team:{' '}
        <span className='text-zinc-300' data-testid='pipeline-owner'>
          {ownerTeam?.name ?? (pipeline.ownerTeamId || 'none')}
        </span>
      </p>
    );
  }

  return (
    <label className='block pt-1'>
      <span>Owning team</span>
      <Select
        value={pipeline.ownerTeamId}
        onChange={(e) => setOwner.mutate(e.target.value)}
        disabled={setOwner.isPending}
        className='mt-1 text-xs'
      >
        <option value=''>No team (org admins and editors only)</option>
        {/* An owner id missing from the list (a deleted team, or the list
            still loading) stays visible rather than reading as "No team". */}
        {pipeline.ownerTeamId && !ownerTeam && (
          <option value={pipeline.ownerTeamId}>{pipeline.ownerTeamId}</option>
        )}
        {teams.map((t) => (
          <option key={t.id} value={t.id}>
            {t.name}
          </option>
        ))}
      </Select>
      <span className='mt-1 block text-muted-3'>
        Members of the owning team may also edit this pipeline.
      </span>
    </label>
  );
}
