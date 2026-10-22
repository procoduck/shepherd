DROP INDEX IF EXISTS agent_tokens_org_id_idx;

ALTER TABLE agent_tokens
    DROP COLUMN IF EXISTS roles,
    DROP COLUMN IF EXISTS clusters,
    DROP COLUMN IF EXISTS org_id;
