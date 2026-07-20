-- +goose Up
ALTER TABLE generation_jobs ADD COLUMN provider_poll_url text;

INSERT INTO providers (id, display_name)
VALUES ('bfl', 'Black Forest Labs')
ON CONFLICT (id) DO UPDATE SET display_name = EXCLUDED.display_name;

-- +goose Down
ALTER TABLE generation_jobs DROP COLUMN IF EXISTS provider_poll_url;
