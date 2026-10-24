ALTER TABLE collectors
    DROP CONSTRAINT collectors_last_refusal_pair,
    DROP COLUMN last_refused_reason,
    DROP COLUMN last_refused_at;
