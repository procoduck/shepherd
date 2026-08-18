ALTER TABLE sessions ALTER COLUMN id_token_expires SET NOT NULL;
ALTER TABLE sessions DROP COLUMN source;
