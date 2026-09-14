-- Generation counter for serve-cache invalidation. Every dirty mark bumps it;
-- a recompute records the generation it read BEFORE loading pipelines and
-- writes only if that generation is still current. Without it, a recompute
-- that started before a newer mark could land its stale content after the
-- mark and clear the dirty flag — the newer recompute then found nothing to
-- do, and collectors were served the stale config until the next change.
ALTER TABLE serve_cache ADD COLUMN dirty_seq bigint NOT NULL DEFAULT 0;
