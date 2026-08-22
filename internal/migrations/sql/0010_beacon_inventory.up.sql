-- 0010_beacon_inventory.up.sql
-- W5 (docs/gateway-tier-plan.md §4, D6): storage for the beacon's projected
-- inventory. This is the ONLY table the beacon ingest path (internal/beacon,
-- internal/agentapi's write handler) is allowed to write to, and its columns
-- are deliberately incapable of holding a raw sample value -- there is no
-- numeric/float column here at all, only identity and a boolean health flag.
-- That is what makes "W5 must not persist raw samples, ever" (plan §5) a
-- schema-level property a reviewer can see, not a promise resting on the Go
-- code never writing one.
--
-- Identity: a remotely-served pipeline runs in an isolated component
-- controller and cannot read the operator's LOCAL remotecfg `id` (see
-- docs/spec.md's "every remote pipeline must be self-contained" note), so
-- collector_instances.id -- the remotecfg wire id -- is not available to the
-- baseline pipeline at render or run time. The next best available identity
-- is what a remote_write payload actually carries: the authenticated agent
-- token (token_id, the same "auth is free" credential D6 names) plus the
-- standard Prometheus `instance` target label prometheus.scrape stamps onto
-- every series it scrapes -- host:port of the reporting Alloy process, stable
-- for the life of that process. (token_id, instance_label) is therefore this
-- table's identity, not a literal FK to collector_instances.
--
-- Expiry: last_seen ages a row out via the sweeper (internal/agentapi's
-- existing background sweep, extended in this slice with
-- DeleteExpiredBeaconInventory) exactly the way collector_instances already
-- ages out stale rows via MarkStaleInstancesInactive/DeleteOldInstances --
-- same pattern, new table, so a collector that stops reporting stops being
-- inventory rather than lingering as a permanently-healthy ghost (plan §4).
--
-- The length CHECKs are defense in depth, mirroring 0009_tenant_routes'
-- documented precedent of a CHECK mirroring a Go-level bound: the body-size
-- cap already bounds a whole request, but a single label value inside an
-- otherwise-small request could still be pathological, and this is a second,
-- independent stop for that even if a future caller skips the Go-level check.
CREATE TABLE beacon_inventory (
    id             uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    token_id       uuid        NOT NULL REFERENCES agent_tokens(id),
    instance_label text        NOT NULL CHECK (length(instance_label) BETWEEN 1 AND 256),
    component_name text        NOT NULL CHECK (length(component_name) BETWEEN 1 AND 256),
    healthy        boolean     NOT NULL,
    last_seen      timestamptz NOT NULL DEFAULT now(),
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (token_id, instance_label, component_name)
);

-- Backs the sweeper's expiry delete.
CREATE INDEX idx_beacon_inventory_last_seen ON beacon_inventory (last_seen);

-- Backs a per-token fleet-health listing (ListBeaconInventoryByToken).
CREATE INDEX idx_beacon_inventory_token ON beacon_inventory (token_id);
