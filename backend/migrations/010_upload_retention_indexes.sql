-- +goose Up
CREATE INDEX upload_sessions_active_idx
    ON upload_sessions (expires_at, owner_user_id)
    WHERE status IN ('created', 'uploading', 'validating');
CREATE INDEX generation_input_assets_asset_idx
    ON generation_input_assets (asset_id);
GRANT DELETE ON upload_sessions TO studio_worker;

-- +goose Down
REVOKE DELETE ON upload_sessions FROM studio_worker;
DROP INDEX IF EXISTS generation_input_assets_asset_idx;
DROP INDEX IF EXISTS upload_sessions_active_idx;
