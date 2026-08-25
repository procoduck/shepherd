-- 0015_local_users.up.sql
-- Local user management, for deployments with no identity provider.
--
-- This is ADDITIVE. It does not change how OIDC works: orgs.admin_group_id,
-- orgs.reader_group_id and teams.idp_group_id keep deciding access for anyone
-- who signs in through an IdP, exactly as before. What is new is a second,
-- parallel path — real user rows with passwords and directly assigned roles —
-- so Shepherd can be run without an IdP at all rather than through a single
-- hardcoded break-glass account.
--
-- The two paths meet only at authorization: internal/auth.authorizeOrgAccess
-- gains a branch (a local session resolves its role from org_members, an OIDC
-- session matches groups as it always has). Neither can silently widen the
-- other, because a session carries exactly one source.
--
-- This is what replaces auth.local_admin.* config. That account was a single
-- identity defined in a ConfigMap/env with unconditional app-admin rights and
-- no way to add a second person, change a password without a redeploy, or tell
-- two operators apart in the audit log. A users table fixes all four.
CREATE TABLE users (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),

    -- login is the username typed at sign-in. Case-insensitively unique: the
    -- alternative is two accounts differing only in capitalisation, which is an
    -- authentication ambiguity rather than a cosmetic one. Stored as entered so
    -- the UI can show what the operator chose.
    login        text        NOT NULL CHECK (length(login) BETWEEN 1 AND 128),

    email        text        NOT NULL DEFAULT '' CHECK (length(email) <= 320),
    display_name text        NOT NULL DEFAULT '' CHECK (length(display_name) <= 256),

    -- argon2id encoded hash, produced by internal/auth.HashPassword. Never a
    -- plaintext password and never a reversible encryption: unlike the OIDC
    -- client secret (which Shepherd must replay to the provider and therefore
    -- encrypts), a login password only ever needs to be COMPARED, so it is
    -- hashed and cannot be recovered even by this process.
    password_hash text       NOT NULL DEFAULT '',

    -- is_app_admin is the global "server admin" flag. Org-scoped roles live in
    -- org_members below; this one is deliberately not an org_members row,
    -- because app-admin is not a membership of anything.
    is_app_admin boolean     NOT NULL DEFAULT false,

    -- must_change_password gates every authenticated route until the user sets
    -- a new one. Set on the seeded first-run admin, and by an admin resetting
    -- someone's password, so a shared temporary password cannot quietly become
    -- a permanent one.
    must_change_password boolean NOT NULL DEFAULT false,

    -- disabled revokes access without deleting the row, so audit_log entries
    -- naming this user keep resolving to something.
    disabled     boolean     NOT NULL DEFAULT false,

    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now(),
    last_login_at timestamptz
);

CREATE UNIQUE INDEX idx_users_login_lower ON users (lower(login));

-- org_members assigns a local user a role within one org.
--
-- The role vocabulary is Grafana's, and `editor` is the point of it: before
-- this there was nothing between viewer (read-only) and admin (everything in
-- the org, including credentials and tenant routes). Most people who need to
-- write a pipeline should not also be able to rotate a tenant route.
CREATE TABLE org_members (
    org_id     uuid        NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    role       text        NOT NULL CHECK (role IN ('admin', 'editor', 'viewer')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, user_id)
);

CREATE INDEX idx_org_members_user ON org_members (user_id);

-- sessions gains the user it belongs to. Nullable because an OIDC session has
-- no local user row: user_oid/email/group_ids already describe that identity,
-- and inventing a users row for every federated principal would make the table
-- a cache of the IdP rather than a record of local accounts.
--
-- ON DELETE CASCADE so deleting a user ends their sessions in the same
-- statement. A revoked account whose cookie still works until it expires is
-- the kind of gap that only shows up when it matters.
ALTER TABLE sessions
    ADD COLUMN user_id uuid REFERENCES users(id) ON DELETE CASCADE;

CREATE INDEX idx_sessions_user_id ON sessions (user_id) WHERE user_id IS NOT NULL;
