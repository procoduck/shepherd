-- 0012_teams_service_accounts.up.sql
-- W10 (docs/gateway-tier-plan.md §4, §3a): closes the "no middle" gap
-- between org-admin (edits everything) and org-reader (nothing) for the
-- service-owner persona, plus the identity/scoping machinery machine
-- callers need.
--
-- teams EXTENDS group_assignments' model rather than replacing it.
-- group_assignments (0001_init.up.sql) ties an IdP group to a *collector*,
-- granting read access to whoever is in that group. teams ties an IdP group
-- to an *org*, and — via pipelines.owner_team_id below — to the pipelines
-- that group's members may WRITE. Same shape (a group_id column, resolved
-- against the session's GroupIDs at authz time — see
-- internal/auth.authorizeOrgAccess's team-membership fallback), one level
-- up: group_assignments answers "can you read this collector's config",
-- teams answers "do you own this pipeline".
--
-- idp_group_id has no UNIQUE-across-orgs constraint: the same Entra group
-- could plausibly own resources in more than one org (a platform team that
-- also owns one tenant's onboarding pipeline), so uniqueness is scoped to
-- (org_id, idp_group_id) — one team per group per org, not one team per
-- group globally. Open decision #8 in the plan (cross-org teams) is
-- deliberately NOT resolved here: teams are org-scoped, full stop; a
-- cross-org team is a distinct future feature, not this table reinterpreted.
CREATE TABLE teams (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id       uuid        NOT NULL REFERENCES orgs(id),
    name         text        NOT NULL,
    idp_group_id text        NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name),
    UNIQUE (org_id, idp_group_id)
);

-- Ownership is the thing scoped write (G11) is scoped BY: a pipeline with a
-- NULL owner_team_id is unowned — org-admin-only, preserving today's
-- behavior exactly for every pipeline that existed before this migration
-- (backfilled implicitly to NULL) and for platform-owned pipelines created
-- after it. ON DELETE SET NULL (not CASCADE, not RESTRICT): deleting a team
-- must not delete — or block deleting — the pipelines it owned; it demotes
-- them back to unowned/admin-only, the same fail-safe direction
-- 0008_destination_bindings' RESTRICT-not-CASCADE choice took (loud failure
-- over silent data loss) but pointed the other way, because here the
-- referencing row (the pipeline) is the thing worth keeping.
ALTER TABLE pipelines
    ADD COLUMN owner_team_id uuid REFERENCES teams(id) ON DELETE SET NULL;

CREATE INDEX idx_pipelines_owner_team ON pipelines (owner_team_id);

-- service_accounts are the machine-caller identity for shepherd.mgmt.v1:
-- same Basic-auth shape as agent_tokens (0001_init.up.sql, id:secret,
-- sha256(secret) stored — never the plaintext), but org-scoped (a service
-- account acts for exactly one org, never fleet-wide) and capability-scoped
-- rather than role-scoped. capability is the propose-vs-apply axis G12
-- exists to prove: 'propose' can read and simulate but never reaches a
-- mutating write path; 'apply' can. There is deliberately no 'org-admin'
-- equivalent capability — a service account is never granted app-admin or
-- cross-org reach, by construction (org_id is a single required column, not
-- nullable-for-global).
CREATE TABLE service_accounts (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id     uuid        NOT NULL REFERENCES orgs(id),
    name       text        NOT NULL,
    capability text        NOT NULL CHECK (capability IN ('propose', 'apply')),
    token_hash bytea       NOT NULL,
    created_by text        NOT NULL DEFAULT '',
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

-- G13: two-part attribution. on_behalf_of is the human a machine actor's
-- write is performed for; NULL for a human session's own actions (there is
-- no delegation to record) and for a machine action Shepherd decided NOT to
-- attribute to any human (see internal/mgmtapi's requireWriteAuthorized doc
-- comment for the reject-vs-record-as-unattributed decision this repo
-- made — short version: Shepherd rejects a machine write that arrives with
-- no on-behalf-of rather than persist one, so in practice a
-- 'service_account' actor_type row here always has on_behalf_of set; the
-- column stays nullable because the CHECK duplicating that rule at the
-- database layer would need to know actor_type-conditional logic a plain
-- CHECK cannot express cleanly, and the Go-level guard is what actually
-- runs before any write, not this table).
ALTER TABLE audit_log
    ADD COLUMN on_behalf_of text;
