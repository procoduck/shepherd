-- 0006_git_providers.down.sql
-- Reverts 0006_git_providers.up.sql.
--
-- WARNING: this is a best-effort reversal, same class of release constraint as
-- 0005_visual_source.down.sql. repo_links.project/repository are reconstructed by
-- splitting repo_url on the matching git_credentials.ado_org_url prefix, which only
-- works for rows that are still shaped like the original ADO backfill (an
-- "<ado_org_url>/<project>/_git/<repository>" URL against a credential that still has
-- ado_org_url set). Once any repo_link points at a non-ADO repo_url, or any credential
-- uses a kind other than 'ado_sp' with no ado_org_url, downgrading will either leave
-- project/repository NULL (rejected by the restored NOT NULL constraint) or produce
-- garbage. Before running this migration down, ensure every repo_link/credential pair is
-- still in the original ADO shape, or manually fix up project/repository afterward.

ALTER TABLE repo_links ADD COLUMN project text;
ALTER TABLE repo_links ADD COLUMN repository text;

UPDATE repo_links l
SET project = split_part(
                 substr(l.repo_url, length(rtrim(c.ado_org_url, '/')) + 2),
                 '/_git/', 1),
    repository = split_part(
                 substr(l.repo_url, length(rtrim(c.ado_org_url, '/')) + 2),
                 '/_git/', 2)
FROM git_credentials c
WHERE c.id = l.credential_id
  AND c.ado_org_url IS NOT NULL
  AND l.repo_url LIKE rtrim(c.ado_org_url, '/') || '/%/_git/%';

ALTER TABLE repo_links ALTER COLUMN project SET NOT NULL;
ALTER TABLE repo_links ALTER COLUMN repository SET NOT NULL;
ALTER TABLE repo_links DROP COLUMN repo_url;

ALTER TABLE git_credentials
  DROP COLUMN kind,
  DROP COLUMN username,
  DROP COLUMN secret2_enc,
  DROP COLUMN provider_config,
  DROP COLUMN ssh_known_hosts,
  DROP COLUMN ca_cert,
  DROP COLUMN tls_insecure_skip_verify,
  ALTER COLUMN ado_org_url SET NOT NULL,
  ALTER COLUMN entra_tenant_id SET NOT NULL,
  ALTER COLUMN client_id SET NOT NULL;

ALTER TABLE git_credentials RENAME TO ado_credentials;
