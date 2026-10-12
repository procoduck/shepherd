import { useQuery } from '@tanstack/react-query';
import { clients } from '@/api/transport';
import type { AuditEntry } from '@/gen/shepherd/mgmt/v1/audit_pb';
import { useMe } from './useMe';
import { useOrg } from './useOrg';

/**
 * Names for what the audit log records by id (#309). An audit row carries only
 * a resource type and id, so the page showed a truncated UUID for things the
 * reader could name. In an org's view the org's own lists resolve pipelines,
 * destinations, teams and collectors — the same queries (and cache keys) their
 * pages use, fetched only when a row of that type is on screen. Organisations
 * resolve to their display name in either scope. A resource that has since
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
  const { data: me } = useMe();
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
  const collectors = useQuery({
    queryKey: ['collectors', orgId],
    queryFn: () => clients.fleet.listCollectors({ orgId }),
    enabled: wants('collector'),
  });
  // Every org, for an app admin's global scope: rows come from orgs the
  // admin need not belong to, which /api/me does not list.
  const allOrgs = useQuery({
    queryKey: ['orgs'],
    queryFn: () => clients.admin.listOrgs({}),
    enabled: !!me?.isAppAdmin && (!orgScoped || types.has('org')),
  });

  const orgNames = new Map<string, string>();
  for (const o of allOrgs.data?.items ?? []) orgNames.set(o.id, o.displayName || o.name);
  for (const o of orgs) orgNames.set(o.id, o.displayName || o.name);

  const names = new Map<string, string>();
  for (const p of pipelines.data?.items ?? []) names.set(`pipeline:${p.id}`, p.name);
  for (const d of destinations.data?.items ?? []) names.set(`destination:${d.id}`, d.name);
  for (const t of teams.data?.items ?? []) names.set(`team:${t.id}`, t.name);
  for (const c of collectors.data?.items ?? []) {
    names.set(`collector:${c.id}`, `${c.cluster} / ${c.role}`);
  }

  return {
    resourceName: (entry) =>
      entry.resourceType === 'org'
        ? orgNames.get(entry.resourceId)
        : names.get(`${entry.resourceType}:${entry.resourceId}`),
    orgName: (id) => orgNames.get(id),
  };
}
