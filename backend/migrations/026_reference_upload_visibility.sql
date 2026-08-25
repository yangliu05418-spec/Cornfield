-- +goose Up
ALTER TABLE upload_sessions
    ADD COLUMN purpose text NOT NULL DEFAULT 'library'
    CHECK (purpose IN ('library', 'reference'));

ALTER TABLE assets
    ADD COLUMN library_visible boolean NOT NULL DEFAULT true;

-- +goose Down
ALTER TABLE assets DROP COLUMN library_visible;
ALTER TABLE upload_sessions DROP COLUMN purpose;
