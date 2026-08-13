-- +goose Up
-- Publishing an existing derived layer is an API-owned metadata operation. Keep
-- the runtime role column-scoped: it may create only the user-visible asset row
-- and attach that row to the selected layer item.
GRANT INSERT (
    owner_user_id,
    kind,
    storage_key,
    sha256,
    media_type,
    original_filename,
    width,
    height,
    byte_size,
    blur_data_url
) ON assets TO studio_api;
GRANT UPDATE (published_asset_id) ON layer_set_items TO studio_api;

-- +goose Down
REVOKE INSERT (
    owner_user_id,
    kind,
    storage_key,
    sha256,
    media_type,
    original_filename,
    width,
    height,
    byte_size,
    blur_data_url
) ON assets FROM studio_api;
REVOKE UPDATE (published_asset_id) ON layer_set_items FROM studio_api;
