-- +goose Up
ALTER TABLE generation_batches
    ADD CONSTRAINT generation_batches_capability_snapshot_fk
    FOREIGN KEY (model_id, capability_revision)
    REFERENCES model_capability_versions (model_id, revision);

-- +goose Down
ALTER TABLE generation_batches
    DROP CONSTRAINT IF EXISTS generation_batches_capability_snapshot_fk;
