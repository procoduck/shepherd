import { useQueries, useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import type { AuditEntry } from '@/gen/shepherd/mgmt/v1/audit_pb';
import { useOrg } from './useOrg';

/**
 * Names for what the audit log records by id (#309). An audit row carries only
 * a resource type and id, so the page showed a truncated UUID for things the
 * reader could name. In an org's view the org's own lists resolve pipelines,
 * destinations, teams and collectors — the same queries (and cache keys) their
 * pages use, fetched only when a row of that type is on screen. Organisations
 * resolve to their display name in either scope, from GetMe. A resource that has since
 * been deleted has no name and keeps its shortened id.
 */
export function useAuditNames(
  orgId: string,
  entries: AuditEntry[],
  orgScoped: boolean,
): {
  resourceName: (entry: AuditEntry) => string | undefined;
  orgName: (id: string) => string | undefined;
} {
  const { orgs } = useOrg();
  const types = new Set(entries.map((e) => e.resourceType));
  const wants = (type: string) => orgScoped && !!orgId && types.has(type);

  const pipelines = useQuery({
    queryKey: ['pipelines', orgId],
    queryFn: () => clients.pipeline.listPipelines({ orgId }),
    enabled: wants('pipeline'),
  });
  const destinations = useQuery({
    queryKey: ['destinations', orgId],
    queryFn: () => clients.destination.listDestinations({ orgId }),
    enabled: wants('destination'),
  });
  const teams = useQuery({
    queryKey: ['teams', orgId],
    queryFn: () => clients.team.listTeams({ orgId }),
    enabled: wants('team'),
  });
  // team.member.add/remove record "<team id>/<user id>" (rpc_team.go): the
  // team resolves from the list above, the user from that team's members —
  // the same query (and key) the Teams page's members dialog uses. A member
  // since removed keeps a shortened user id.
  const memberTeamIds = orgScoped
    ? [
        ...new Set(
          entries
            .filter((e) => e.resourceType === 'team' && e.resourceId.includes('/'))
            .map((e) => e.resourceId.split('/')[0]),
        ),
      ]
    : [];
  const members = useQueries({
    queries: memberTeamIds.map((teamId) => ({
      queryKey: ['team-members', teamId],
      queryFn: () => clients.team.listTeamMembers({ orgId, teamId }),
      enabled: !!orgId,
    })),
  });
  const collectors = useQuery({
    queryKey: ['collectors', orgId],
    queryFn: () => clients.fleet.listCollectors({ orgId }),
    enabled: wants('collector'),
  });
  // GetMe's orgs are enough: for an app admin — the only caller with a global
  // scope — it lists every org on the server (rpc_me.go), not only theirs.
  const orgNames = new Map<string, string>();
  for (const o of orgs) orgNames.set(o.id, o.displayName || o.name);

  const names = new Map<string, string>();
  for (const p of pipelines.data?.items ?? []) names.set(`pipeline:${p.id}`, p.name);
  for (const d of destinations.data?.items ?? []) names.set(`destination:${d.id}`, d.name);
  for (const t of teams.data?.items ?? []) names.set(`team:${t.id}`, t.name);
  const memberNames = new Map<string, string>();
  memberTeamIds.forEach((teamId, i) => {
    for (const m of members[i]?.data?.items ?? []) {
      memberNames.set(`${teamId}/${m.userId}`, m.displayName || m.login || m.email);
    }
  });
  const teamMemberName = (resourceId: string): string | undefined => {
    const [teamId, userId = ''] = resourceId.split('/');
    const team = names.get(`team:${teamId}`);
    if (!team) return undefined;
    const user =
      memberNames.get(resourceId) || (userId.length > 13 ? `${userId.slice(0, 8)}…` : userId);
    return `${team} / ${user}`;
  };
  for (const c of collectors.data?.items ?? []) {
    names.set(`collector:${c.id}`, `${c.cluster} / ${c.role}`);
  }

  return {
    resourceName: (entry) =>
      entry.resourceType === 'org'
        ? orgNames.get(entry.resourceId)
        : entry.resourceType === 'team' && entry.resourceId.includes('/')
          ? teamMemberName(entry.resourceId)
          : names.get(`${entry.resourceType}:${entry.resourceId}`),
    orgName: (id) => orgNames.get(id),
  };
}
