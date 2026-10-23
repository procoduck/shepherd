-- Bind agent tokens to an organisation, in the same shape as an OIDC
-- collector binding (0021_agent_identities): the org a token acts for, plus
-- optional cluster/role allowlists (empty array = any within the org).
--
-- org_id is nullable on purpose: rows that exist before this migration are
-- left unbound rather than guessed at. An unbound token still authenticates,
-- but the agent API refuses every collector RPC it makes until an app admin
-- binds it (BindAgentToken / `shepherd token bind`). Every token created
-- after this migration is bound at creation — the API and CLI require an org.
--
-- ON DELETE SET NULL: deleting an org leaves its tokens' rows (and their
-- audit history) in place but unbound, so they stop working instead of
-- blocking the delete.
ALTER TABLE agent_tokens
    ADD COLUMN org_id   uuid  REFERENCES orgs(id) ON DELETE SET NULL,
    ADD COLUMN clusters jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(clusters) = 'array'),
    ADD COLUMN roles    jsonb NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(roles) = 'array'),
    -- may_claim: whether the token may claim an unclaimed cluster for its org
    -- on a collector's first poll. True only for a token an app admin minted
    -- or bound (and the operator CLI); a token an org admin or a service
    -- account minted serves only clusters its org has already claimed —
    -- claiming a cluster into an org stays an app-admin decision.
    ADD COLUMN may_claim boolean NOT NULL DEFAULT false;

CREATE INDEX agent_tokens_org_id_idx ON agent_tokens (org_id);
