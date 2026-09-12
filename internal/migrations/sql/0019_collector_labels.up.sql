ALTER TABLE collectors ADD COLUMN labels jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(labels) = 'object');
