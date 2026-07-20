-- +goose Up
ALTER TABLE generation_jobs ADD COLUMN provider_poll_url text;

INSERT INTO providers (id, name)
VALUES ('bfl', 'Black Forest Labs')
ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name;

-- +goose Down
ALTER TABLE generation_jobs DROP COLUMN IF EXISTS provider_poll_url;
