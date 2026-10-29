-- 0053_group_assignment_org.up.sql
-- Binds every group assignment to the org that granted it. Before 0053 a row
-- carried only (collector_id, group_id), and access checks read the
-- collector's CURRENT org, so a grant made while a cluster belonged to one org
-- kept working after the cluster moved to another. From 0053 a grant counts
-- only while group_assignments.org_id is the collector's current org, and
-- every path that changes a cluster's org removes the grants the new org did
-- not make (internal/store/queries/clusters.sql).
--
-- Backfill (hybrid): a row is bound to its collector's current org unless
--   * the collector's cluster has no org, or
--   * the audit log records a cluster.claim or cluster.unclaim of that
--     cluster after the row was created (the only actions that change a
--     cluster's org and are audited: AdminService ClaimCluster/UnclaimCluster;
--     agent auto-claim only binds an UNclaimed cluster, which has no grants
--     to carry). A re-claim into the same org also counts: the audit log
--     cannot tell it apart from a move, and a removed grant can be re-created
--     while a carried-over one cannot be told apart from a legitimate one.
-- Unbound rows are recorded in the audit log, then deleted. Each audit row is
-- written as the system actor 'migration:0053' with the shape
-- FleetService.DeleteAssignment writes (action assignment.delete, resource
-- the collector, detail.group_id), plus the reason and, when the log knows
-- it, the org that granted the row (its latest assignment.create entry); the
-- row is filed under that org so its admins see it.
-- A grant whose org is deleted grants nothing, so it goes with the org.
ALTER TABLE group_assignments ADD COLUMN org_id uuid REFERENCES orgs(id) ON DELETE CASCADE;

UPDATE group_assignments ga
SET org_id = cl.org_id
FROM collectors c
JOIN clusters cl ON cl.id = c.cluster_id
WHERE c.id = ga.collector_id
  AND cl.org_id IS NOT NULL
  AND NOT EXISTS (
      SELECT 1 FROM audit_log a
      WHERE a.resource_type = 'cluster'
        AND a.resource_id = cl.id::text
        AND a.action IN ('cluster.claim', 'cluster.unclaim')
        AND a.at > ga.created_at
  );

INSERT INTO audit_log (actor, actor_type, org_id, action, resource_type, resource_id, detail)
SELECT 'migration:0053',
       'system',
       granted.org_id,
       'assignment.delete',
       'collector',
       ga.collector_id::text,
       jsonb_build_object(
           'group_id', ga.group_id,
           'group_display_name', ga.group_display_name,
           'cluster_id', c.cluster_id::text,
           'previous_org_id', granted.org_id::text,
           'current_org_id', cl.org_id::text,
           'assignment_created_at', ga.created_at,
           'reason', CASE WHEN cl.org_id IS NULL THEN 'cluster has no org' ELSE 'org changed' END
       )
FROM group_assignments ga
JOIN collectors c ON c.id = ga.collector_id
JOIN clusters cl ON cl.id = c.cluster_id
LEFT JOIN LATERAL (
    SELECT a.org_id
    FROM audit_log a
    WHERE a.action = 'assignment.create'
      AND a.resource_type = 'collector'
      AND a.resource_id = ga.collector_id::text
      AND a.detail ->> 'group_id' = ga.group_id
    ORDER BY a.at DESC, a.id DESC
    LIMIT 1
) granted ON true
WHERE ga.org_id IS NULL;

DELETE FROM group_assignments WHERE org_id IS NULL;

ALTER TABLE group_assignments ALTER COLUMN org_id SET NOT NULL;

-- The viewer-floor check (ListCollectorIDsByGroupMembershipInOrg) filters on
-- (org_id, group_id).
CREATE INDEX idx_group_assignments_org_group ON group_assignments (org_id, group_id);

-- Rolling-update compatibility: a v0.21 binary's CreateGroupAssignment names
-- no org_id, so NOT NULL refuses it and that pod's Access-tab create fails
-- until the rollout completes. That is deliberate: filling the org in for it
-- (a trigger reading the collector's current org) could bind a grant to an org
-- the cluster moved to after the old pod's ownership check.
