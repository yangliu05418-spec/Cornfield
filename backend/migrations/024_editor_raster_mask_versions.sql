-- +goose Up
CREATE TABLE editor_raster_masks (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    editor_project_id uuid NOT NULL REFERENCES image_editor_projects(id) ON DELETE CASCADE,
    target_node_id text NOT NULL CHECK (char_length(target_node_id) BETWEEN 1 AND 64),
    source_asset_id uuid NOT NULL REFERENCES assets(id),
    width integer NOT NULL CHECK (width BETWEEN 1 AND 8192),
    height integer NOT NULL CHECK (height BETWEEN 1 AND 8192),
    default_alpha smallint NOT NULL DEFAULT 255 CHECK (default_alpha BETWEEN 0 AND 255),
    current_version bigint NOT NULL DEFAULT 0 CHECK (current_version >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (width::bigint * height::bigint <= 36000000),
    UNIQUE (editor_project_id, target_node_id)
);

CREATE TABLE editor_raster_mask_versions (
    mask_id uuid NOT NULL REFERENCES editor_raster_masks(id) ON DELETE CASCADE,
    version bigint NOT NULL CHECK (version >= 0),
    base_version bigint,
    project_revision bigint NOT NULL CHECK (project_revision >= 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (mask_id, version),
    CHECK ((version = 0 AND base_version IS NULL) OR (version > 0 AND base_version >= 0 AND base_version < version)),
    FOREIGN KEY (mask_id, base_version) REFERENCES editor_raster_mask_versions(mask_id, version)
);

CREATE TABLE editor_raster_mask_version_tiles (
    mask_id uuid NOT NULL,
    version bigint NOT NULL,
    tile_x smallint NOT NULL CHECK (tile_x BETWEEN 0 AND 31),
    tile_y smallint NOT NULL CHECK (tile_y BETWEEN 0 AND 31),
    width smallint NOT NULL CHECK (width BETWEEN 1 AND 256),
    height smallint NOT NULL CHECK (height BETWEEN 1 AND 256),
    storage_key text NOT NULL,
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    byte_size integer NOT NULL CHECK (byte_size = width::integer * height::integer),
    PRIMARY KEY (mask_id, version, tile_x, tile_y),
    FOREIGN KEY (mask_id, version) REFERENCES editor_raster_mask_versions(mask_id, version) ON DELETE CASCADE
);
CREATE INDEX editor_raster_mask_tiles_digest_idx
    ON editor_raster_mask_version_tiles(sha256);

-- A short database-visible lease closes the API-put / Worker-delete race while
-- immutable bytes are committed before their authoritative manifest row.
CREATE TABLE blob_write_leases (
    id uuid PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES users(id),
    sha256 text NOT NULL CHECK (sha256 ~ '^[0-9a-f]{64}$'),
    storage_key text NOT NULL,
    expires_at timestamptz NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (expires_at > created_at)
);
CREATE INDEX blob_write_leases_digest_idx ON blob_write_leases(sha256, expires_at);
CREATE INDEX blob_write_leases_expiry_idx ON blob_write_leases(expires_at);

GRANT SELECT, INSERT, UPDATE, DELETE ON editor_raster_masks TO studio_api;
GRANT SELECT, INSERT ON editor_raster_mask_versions, editor_raster_mask_version_tiles TO studio_api;
GRANT SELECT, INSERT, DELETE ON blob_write_leases TO studio_api;
GRANT SELECT ON editor_raster_masks, editor_raster_mask_versions, editor_raster_mask_version_tiles TO studio_worker;
GRANT SELECT, DELETE ON blob_write_leases TO studio_worker;

-- +goose Down
REVOKE ALL ON blob_write_leases FROM studio_api, studio_worker;
REVOKE ALL ON editor_raster_masks, editor_raster_mask_versions, editor_raster_mask_version_tiles FROM studio_api, studio_worker;
DROP TABLE blob_write_leases;
DROP TABLE editor_raster_mask_version_tiles;
DROP TABLE editor_raster_mask_versions;
DROP TABLE editor_raster_masks;
