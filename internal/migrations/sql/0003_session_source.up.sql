ALTER TABLE sessions ADD COLUMN source text NOT NULL DEFAULT 'oidc';
ALTER TABLE sessions ALTER COLUMN id_token_expires DROP NOT NULL;
