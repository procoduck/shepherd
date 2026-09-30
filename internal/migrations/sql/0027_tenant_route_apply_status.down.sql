-- 0027_tenant_route_apply_status.down.sql
ALTER TABLE tenant_routes
    DROP COLUMN apply_status,
    DROP COLUMN apply_message,
    DROP COLUMN applied_at;
