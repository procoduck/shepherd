-- Enable pgcrypto for gen_random_uuid()
CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE orgs (
    id              uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name            text        NOT NULL UNIQUE,
    display_name    text        NOT NULL,
    admin_group_id  text        NOT NULL,
    reader_group_id text,
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE clusters (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL UNIQUE,
    org_id     uuid        REFERENCES orgs(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE collectors (
    id         uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    cluster_id uuid        NOT NULL REFERENCES clusters(id),
    role       text        NOT NULL CHECK (role IN ('metrics', 'logs', 'singleton', 'receiver')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (cluster_id, role)
);

CREATE TABLE collector_instances (
    id                    text        PRIMARY KEY,
    collector_id          uuid        NOT NULL REFERENCES collectors(id),
    name                  text        NOT NULL,
    local_attributes      jsonb       NOT NULL DEFAULT '{}',
    alloy_version         text,
    os                    text,
    last_seen             timestamptz,
    unregistered_at       timestamptz,
    remote_config_status  text,
    remote_config_error   text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_collector_instances_collector_last_seen
    ON collector_instances (collector_id, last_seen);

CREATE TABLE group_assignments (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    collector_id         uuid NOT NULL REFERENCES collectors(id),
    group_id             text NOT NULL,
    group_display_name   text NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now(),
    UNIQUE (collector_id, group_id)
);

CREATE TABLE destinations (
    id               uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id           uuid        NOT NULL REFERENCES orgs(id),
    name             text        NOT NULL,
    type             text        NOT NULL CHECK (type IN ('prometheus', 'loki', 'otlp')),
    url              text        NOT NULL,
    tenant_id        text        NOT NULL DEFAULT '',
    secret_name      text        NOT NULL,
    secret_namespace text        NOT NULL,
    auth_mode        text        NOT NULL CHECK (auth_mode IN ('none', 'oauth2_secret', 'basic_secret')),
    extra            jsonb       NOT NULL DEFAULT '{}',
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

CREATE TABLE pipelines (
    id            uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id        uuid        NOT NULL REFERENCES orgs(id),
    name          text        NOT NULL,
    contents      text        NOT NULL DEFAULT '',
    matchers      jsonb       NOT NULL DEFAULT '[]',
    enabled       boolean     NOT NULL DEFAULT false,
    source        text        NOT NULL CHECK (source IN ('ui', 'wizard', 'git')),
    wizard_kind   text,
    wizard_state  jsonb,
    repo_link_id  uuid,
    git_path      text,
    created_by    text        NOT NULL DEFAULT '',
    updated_by    text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

CREATE INDEX idx_pipelines_org_enabled ON pipelines (org_id, enabled);

CREATE TABLE pipeline_revisions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id  uuid        NOT NULL REFERENCES pipelines(id) ON DELETE CASCADE,
    revision     int         NOT NULL,
    contents     text        NOT NULL DEFAULT '',
    matchers     jsonb       NOT NULL DEFAULT '[]',
    enabled      boolean     NOT NULL DEFAULT false,
    changed_by   text        NOT NULL DEFAULT '',
    changed_at   timestamptz NOT NULL DEFAULT now(),
    change_note  text        NOT NULL DEFAULT '',
    UNIQUE (pipeline_id, revision)
);

CREATE TABLE ado_credentials (
    id                 uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id             uuid        NOT NULL REFERENCES orgs(id),
    name               text        NOT NULL,
    ado_org_url        text        NOT NULL,
    entra_tenant_id    text        NOT NULL,
    client_id          text        NOT NULL,
    client_secret_enc  bytea       NOT NULL,
    created_at         timestamptz NOT NULL DEFAULT now(),
    updated_at         timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, name)
);

CREATE TABLE repo_links (
    id                    uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id                uuid        NOT NULL REFERENCES orgs(id),
    collector_id          uuid        NOT NULL REFERENCES collectors(id),
    credential_id         uuid        NOT NULL REFERENCES ado_credentials(id),
    project               text        NOT NULL,
    repository            text        NOT NULL,
    branch                text        NOT NULL DEFAULT 'main',
    path                  text        NOT NULL DEFAULT '/',
    poll_interval_seconds int         NOT NULL DEFAULT 180,
    last_synced_at        timestamptz,
    last_commit           text,
    sync_status           text,
    sync_error            text,
    created_at            timestamptz NOT NULL DEFAULT now(),
    updated_at            timestamptz NOT NULL DEFAULT now()
);

ALTER TABLE pipelines
    ADD CONSTRAINT fk_pipelines_repo_link
    FOREIGN KEY (repo_link_id) REFERENCES repo_links(id);

CREATE TABLE agent_tokens (
    id          uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    name        text        NOT NULL,
    token_hash  bytea       NOT NULL,
    created_by  text        NOT NULL DEFAULT '',
    revoked_at  timestamptz,
    created_at  timestamptz NOT NULL DEFAULT now(),
    updated_at  timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE serve_cache (
    collector_id  uuid        PRIMARY KEY REFERENCES collectors(id),
    content       text        NOT NULL DEFAULT '',
    hash          text        NOT NULL DEFAULT '',
    computed_at   timestamptz NOT NULL DEFAULT now(),
    dirty         boolean     NOT NULL DEFAULT true
);

CREATE TABLE sessions (
    id                  text        PRIMARY KEY,
    user_oid            text        NOT NULL,
    email               text        NOT NULL,
    display_name        text        NOT NULL,
    group_ids           jsonb       NOT NULL DEFAULT '[]',
    is_app_admin        boolean     NOT NULL DEFAULT false,
    id_token_expires    timestamptz NOT NULL,
    expires_at          timestamptz NOT NULL,
    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX idx_sessions_expires_at ON sessions (expires_at);

CREATE TABLE audit_log (
    id            bigserial   PRIMARY KEY,
    at            timestamptz NOT NULL DEFAULT now(),
    actor         text        NOT NULL,
    actor_type    text        NOT NULL,
    org_id        uuid,
    action        text        NOT NULL,
    resource_type text        NOT NULL,
    resource_id   text        NOT NULL DEFAULT '',
    detail        jsonb       NOT NULL DEFAULT '{}'
);

CREATE INDEX idx_audit_log_org_at ON audit_log (org_id, at);
