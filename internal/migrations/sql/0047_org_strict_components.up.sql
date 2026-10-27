-- Strict tenant mode accepts only classified components (#421).
--
-- In strict tenant mode a pipeline may use a built-in component only when the
-- schema overlay classifies it allowed or checked
-- (internal/schema/artifacts/overlay.json, validate.ComponentClassifications).
-- An application administrator may allow one more component for one org
-- (AdminService.Add/Remove/ListOrgStrictComponents): a component classified
-- refused, or one no shipped schema classifies. That lifts only the
-- component refusal; every other strict check still applies to it, and the UI
-- and audit log show it as admin-allowed and unreviewed. Removing an entry is
-- refused while the strict tenant rule applies to the org and an enabled
-- pipeline uses the component.
CREATE TABLE org_strict_components (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    org_id uuid NOT NULL REFERENCES orgs (id) ON DELETE CASCADE,
    component text COLLATE "C" NOT NULL
        CHECK (length(component) <= 128 AND component ~ '^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$'),
    created_by text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (org_id, component)
);
