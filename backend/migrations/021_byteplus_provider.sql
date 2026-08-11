-- +goose Up
INSERT INTO providers (id, display_name)
VALUES ('byteplus', 'BytePlus ModelArk')
ON CONFLICT (id) DO UPDATE SET display_name = EXCLUDED.display_name;

-- +goose Down
DELETE FROM providers
WHERE id = 'byteplus'
  AND NOT EXISTS (SELECT 1 FROM models WHERE provider_id = providers.id)
  AND NOT EXISTS (SELECT 1 FROM generation_jobs WHERE provider_id = providers.id)
  AND NOT EXISTS (SELECT 1 FROM provider_attempts WHERE provider_id = providers.id);
