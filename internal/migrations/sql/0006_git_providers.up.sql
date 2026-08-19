-- 0006_git_providers.up.sql
-- F9: standard-git GitOps with pluggable provider auth (docs/git-provider-design.md §3.3).
--
-- ado_credentials becomes git_credentials: a `kind` selects one of six auth strategies
-- (none/basic/pat/ssh/ado_sp/github_app). The ADO-specific columns (ado_org_url,
-- entra_tenant_id, client_id) stay as real columns, now nullable, purely so the backfill
-- below can derive the ADO clone URL; new providers use provider_config (JSONB) instead of
-- growing new columns. client_secret_enc keeps its name and is now "the one secret" for
-- whichever kind the row is (password | PAT | client secret | GitHub App private key | SSH
-- private key); secret2_enc is an optional second secret (e.g. an SSH key passphrase).
--
-- repo_links gains repo_url, an ordinary git clone URL, backfilled from the ADO
-- org/project/repository triple for every existing row, then drops project/repository.

ALTER TABLE ado_credentials RENAME TO git_credentials;

ALTER TABLE git_credentials
  ADD COLUMN kind text NOT NULL DEFAULT 'ado_sp'
      CHECK (kind IN ('none', 'basic', 'pat', 'ssh', 'ado_sp', 'github_app')),
  ADD COLUMN username text,
  ADD COLUMN secret2_enc bytea,
  ADD COLUMN provider_config jsonb NOT NULL DEFAULT '{}',
  ADD COLUMN ssh_known_hosts text,
  ADD COLUMN ca_cert text,
  ADD COLUMN tls_insecure_skip_verify boolean NOT NULL DEFAULT false,
  ALTER COLUMN ado_org_url DROP NOT NULL,
  ALTER COLUMN entra_tenant_id DROP NOT NULL,
  ALTER COLUMN client_id DROP NOT NULL;

ALTER TABLE repo_links ADD COLUMN repo_url text;

UPDATE repo_links l
SET repo_url = rtrim(c.ado_org_url, '/') || '/' || l.project || '/_git/' || l.repository
FROM git_credentials c
WHERE c.id = l.credential_id;

ALTER TABLE repo_links ALTER COLUMN repo_url SET NOT NULL;
ALTER TABLE repo_links DROP COLUMN project;
ALTER TABLE repo_links DROP COLUMN repository;
