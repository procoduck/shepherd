-- 0014_oidc_settings.up.sql
-- UI-configurable OIDC: an app admin can point Shepherd at an identity
-- provider from the admin UI when the Helm chart did not already configure
-- one. Helm/env config (config.OIDCConfig, SHEPHERD_OIDC_*) always wins --
-- see internal/auth.Handler.Reload -- so this table is the SECOND-choice
-- source, never an override. That ordering is deliberate: a cluster whose
-- identity provider is declared in git must not be silently re-pointed by
-- whoever holds an app-admin session.
--
-- Singleton by construction. `id boolean PRIMARY KEY DEFAULT true CHECK (id)`
-- is the standard one-row idiom: the only value the primary key can hold is
-- true, so a second INSERT conflicts rather than creating a second identity
-- provider nobody chose between. Shepherd authenticates against exactly one
-- IdP; a list would need a provider-selection UI on the login page and an
-- answer for which one owns a given session, neither of which this feature
-- has a use case for.
--
-- Secret storage follows git_credentials.client_secret_enc (0006) and
-- grafana_connections.token_enc (0011), not destinations' secret_name /
-- secret_namespace reference: Shepherd itself performs the OIDC token
-- exchange, so it must hold the plaintext client secret in memory at call
-- time. AES-256-GCM at rest via internal/crypto, decrypted only in-process,
-- never returned to a caller -- the API answers client_secret_set: true and
-- nothing more (internal/mgmtapi/rpc_admin_oidc.go).
CREATE TABLE oidc_settings (
    id                boolean     PRIMARY KEY DEFAULT true CHECK (id),

    -- enabled is the operator's explicit "this IdP is live" switch, separate
    -- from "a row exists". An admin saves and tests a provider first, then
    -- turns it on; a provider that starts failing can be switched off
    -- without destroying the configuration needed to debug it.
    enabled           boolean     NOT NULL DEFAULT false,

    -- provider is a preset key from internal/auth.Presets ("entra", "okta",
    -- "google", "auth0", "keycloak", "cognito", "gitlab", "authentik",
    -- "generic"). It selects claim/scope defaults in the UI and picks the
    -- Entra-specific group path below; it is NOT trusted for anything
    -- security-relevant beyond that -- every field it prefills is stored
    -- explicitly in this row, so the effective config is readable here
    -- without consulting the preset table.
    provider          text        NOT NULL DEFAULT 'generic'
                                  CHECK (length(provider) BETWEEN 1 AND 64),

    -- display_name is the login button label ("Continue with Okta").
    display_name      text        NOT NULL DEFAULT ''
                                  CHECK (length(display_name) <= 128),

    -- issuer must be https: an http issuer would carry the discovery
    -- document, and therefore the JWKS location, over a channel an attacker
    -- can rewrite. The Go validator (internal/auth.ValidateSettings) produces
    -- the good error message; this CHECK is what makes a bad value unstorable
    -- if some future write path forgets to call it -- the same
    -- defense-in-depth 0009/0011/0013 apply.
    issuer            text        NOT NULL
                                  CHECK (issuer ~ '^https://[^[:space:]]+$' AND length(issuer) <= 2048),
    client_id         text        NOT NULL
                                  CHECK (length(client_id) BETWEEN 1 AND 512),
    client_secret_enc bytea       NOT NULL,

    -- redirect_url is allowed to be http:// because a local dev / port-forward
    -- deployment legitimately serves the callback over plain HTTP on
    -- localhost. Unlike the issuer, this value is not a channel Shepherd
    -- fetches keys over -- it is a string handed to the IdP, which enforces
    -- its own registered-redirect allowlist on top.
    redirect_url      text        NOT NULL
                                  CHECK (redirect_url ~ '^https?://[^[:space:]]+$' AND length(redirect_url) <= 2048),
    scopes            text[]      NOT NULL DEFAULT ARRAY['openid', 'profile', 'email'],

    -- Claim names. Shepherd's pre-existing Entra-only path hardcoded "oid"
    -- for the subject; standard OIDC is "sub", and that is the default here.
    -- The subject lands in sessions.user_oid, so changing it after the fact
    -- changes who an existing local grant refers to -- which is why it is
    -- stored per-provider rather than sniffed per-login.
    subject_claim     text        NOT NULL DEFAULT 'sub'
                                  CHECK (length(subject_claim) BETWEEN 1 AND 128),
    email_claim       text        NOT NULL DEFAULT 'email'
                                  CHECK (length(email_claim) BETWEEN 1 AND 128),
    name_claim        text        NOT NULL DEFAULT 'name'
                                  CHECK (length(name_claim) BETWEEN 1 AND 128),

    -- groups_claim names the ID-token claim carrying group membership
    -- ("groups" for Okta/Keycloak/Auth0/Authentik/GitLab, "cognito:groups"
    -- for Cognito, "groups" or "roles" for Entra). Its VALUES are matched
    -- against app_admin_groups below and against orgs.admin_group_id /
    -- orgs.reader_group_id -- so for a non-Entra provider those org columns
    -- hold whatever the IdP emits (often a group NAME), not a GUID. Nothing
    -- in the schema assumed a GUID; only the Entra-shaped documentation did.
    groups_claim      text        NOT NULL DEFAULT 'groups'
                                  CHECK (length(groups_claim) BETWEEN 1 AND 128),
    app_admin_groups  text[]      NOT NULL DEFAULT '{}',

    -- use_graph_groups keeps the pre-existing Microsoft Graph
    -- transitiveMemberOf lookup available for Entra, where the groups claim
    -- is omitted entirely once a user is in more than ~200 groups (the
    -- "claim overage" case) -- exactly the tenants where group-based
    -- authorization matters most. Off for every other provider.
    use_graph_groups  boolean     NOT NULL DEFAULT false,
    graph_base_url    text        NOT NULL DEFAULT 'https://graph.microsoft.com'
                                  CHECK (graph_base_url ~ '^https://[^[:space:]]+$' AND length(graph_base_url) <= 2048),

    created_at        timestamptz NOT NULL DEFAULT now(),
    updated_at        timestamptz NOT NULL DEFAULT now(),
    -- updated_by is the actor string (auth.ActorFromCtx) that last wrote this
    -- row. Repointing the identity provider is the single highest-leverage
    -- write in the product; the audit log records it, and this column makes
    -- the current state self-describing without a join.
    updated_by        text        NOT NULL DEFAULT ''
);
