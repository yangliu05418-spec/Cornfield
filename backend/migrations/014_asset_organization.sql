-- +goose Up
CREATE TABLE asset_folders (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);
CREATE UNIQUE INDEX asset_folders_owner_name_idx ON asset_folders(owner_user_id,lower(name));
CREATE INDEX asset_folders_owner_created_idx ON asset_folders(owner_user_id,created_at,id);

ALTER TABLE assets
    ADD COLUMN folder_id uuid REFERENCES asset_folders(id) ON DELETE SET NULL,
    ADD COLUMN archived_at timestamptz;
CREATE INDEX assets_owner_organization_cursor_idx
    ON assets(owner_user_id,folder_id,archived_at,created_at DESC,id DESC)
    WHERE purged_at IS NULL AND purge_pending=false;

GRANT SELECT, INSERT, UPDATE, DELETE ON asset_folders TO studio_api;
GRANT UPDATE (folder_id,archived_at) ON assets TO studio_api;
GRANT DELETE ON asset_folders TO studio_worker;

-- +goose Down
REVOKE ALL ON asset_folders FROM studio_api, studio_worker;
REVOKE UPDATE (folder_id,archived_at) ON assets FROM studio_api;
DROP INDEX IF EXISTS assets_owner_organization_cursor_idx;
ALTER TABLE assets DROP COLUMN IF EXISTS archived_at, DROP COLUMN IF EXISTS folder_id;
DROP TABLE IF EXISTS asset_folders;
