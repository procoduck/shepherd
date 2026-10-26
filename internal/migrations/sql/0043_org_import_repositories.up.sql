-- Pinned import.git for strict tenant mode (#390).
--
-- A strict org may use import.git only when its revision is a full commit
-- SHA (40 lowercase hex digits; Alloy follows a moving tag or branch, so only
-- a commit is immutable) and its repository is on the org's allowlist, which
-- an application administrator keeps (AdminService.Add/Remove/
-- ListOrgImportRepositories). Shepherd fetches the module at save time and
-- runs the same tenant check on its content (internal/pinnedimport).
--
-- repository is the URL as the administrator entered it; repository_key is
-- its normal form (pinnedimport.NormalizeRepository: lower-case scheme and
-- host, no default port, no trailing slash or ".git", the scp-like
-- git@host:path form as ssh://), which a pipeline's import.git repository
-- must match. credential_id optionally names one of the org's git sync
-- credentials (git_credentials, encrypted at rest) for a private
-- repository; it cannot be deleted while an entry uses it.
CREATE TABLE org_import_repositories (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    repository text NOT NULL,
    repository_key text COLLATE "C" NOT NULL,
    credential_id uuid REFERENCES git_credentials (id) ON DELETE RESTRICT,
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, repository_key)
);

-- Fetched modules, by org and (repository, commit, path). A commit cannot
-- change, so a row is never refreshed, only used: a save whose source has
-- since become unreachable still verifies against it (with a warning that
-- collectors may fail to fetch the module when they restart). Keyed per org
-- so one org's credential never yields content to another. Bounded per org
-- (pinnedimport.MaxCachedModules rows, each at most gitsync.max_file_bytes);
-- the least recently used rows go first.
CREATE TABLE pinned_import_modules (
    org_id uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    repository_key text COLLATE "C" NOT NULL,
    revision text COLLATE "C" NOT NULL CHECK (revision ~ '^[0-9a-f]{40}$'),
    path text COLLATE "C" NOT NULL,
    content text NOT NULL,
    fetched_at timestamptz NOT NULL DEFAULT now(),
    used_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (org_id, repository_key, revision, path)
);
CREATE INDEX pinned_import_modules_used_idx ON pinned_import_modules (org_id, used_at);
