ALTER TABLE collectors ADD COLUMN labels jsonb NOT NULL DEFAULT '{}'::jsonb
    CHECK (jsonb_typeof(labels) = 'object')
    CHECK (jsonb_array_length(jsonb_path_query_array(labels, '$.keyvalue()')) <= 64)
    CHECK (pg_column_size(labels) <= 65536);
