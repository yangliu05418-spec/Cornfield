-- +goose Up
ALTER TABLE asset_operations
    ADD COLUMN source_artboard_id text,
    ADD COLUMN export_artboard_ids text[] NOT NULL DEFAULT '{}'::text[],
    ADD COLUMN export_mode text CHECK (export_mode IS NULL OR export_mode IN ('single','composite')),
    ADD COLUMN estimated_wait_lower_seconds integer,
    ADD COLUMN estimated_wait_upper_seconds integer,
    ADD COLUMN estimate_sample_size integer NOT NULL DEFAULT 0 CHECK (estimate_sample_size >= 0),
    ADD CONSTRAINT asset_operations_wait_estimate_check CHECK (
        (estimated_wait_lower_seconds IS NULL AND estimated_wait_upper_seconds IS NULL)
        OR (estimated_wait_lower_seconds > 0 AND estimated_wait_upper_seconds >= estimated_wait_lower_seconds)
    );

ALTER TABLE layer_sets
    ADD COLUMN artboard_id text,
    ADD COLUMN applied_revision bigint CHECK (applied_revision IS NULL OR applied_revision >= 0);

CREATE INDEX asset_operations_layer_estimate_idx
    ON asset_operations(resolution,prompt_optimization_mode,completed_at DESC)
    WHERE operation_type='layer_decomposition' AND status='succeeded';
CREATE INDEX layer_sets_project_artboard_idx
    ON layer_sets(editor_project_id,artboard_id,created_at DESC);

GRANT UPDATE (source_artboard_id,export_artboard_ids,export_mode,
    estimated_wait_lower_seconds,estimated_wait_upper_seconds,estimate_sample_size)
    ON asset_operations TO studio_worker;
GRANT UPDATE (artboard_id,applied_revision) ON layer_sets TO studio_worker;

-- +goose Down
REVOKE UPDATE (artboard_id,applied_revision) ON layer_sets FROM studio_worker;
REVOKE UPDATE (source_artboard_id,export_artboard_ids,export_mode,
    estimated_wait_lower_seconds,estimated_wait_upper_seconds,estimate_sample_size)
    ON asset_operations FROM studio_worker;
DROP INDEX IF EXISTS layer_sets_project_artboard_idx;
DROP INDEX IF EXISTS asset_operations_layer_estimate_idx;
ALTER TABLE layer_sets DROP COLUMN applied_revision, DROP COLUMN artboard_id;
ALTER TABLE asset_operations
    DROP CONSTRAINT asset_operations_wait_estimate_check,
    DROP COLUMN estimate_sample_size,
    DROP COLUMN estimated_wait_upper_seconds,
    DROP COLUMN estimated_wait_lower_seconds,
    DROP COLUMN export_mode,
    DROP COLUMN export_artboard_ids,
    DROP COLUMN source_artboard_id;
