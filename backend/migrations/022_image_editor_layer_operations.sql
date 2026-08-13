-- +goose Up
ALTER TABLE assets DROP CONSTRAINT assets_kind_check;
ALTER TABLE assets ADD CONSTRAINT assets_kind_check
    CHECK (kind IN ('upload','generation','derived','editor'));

CREATE TABLE image_editor_projects (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    source_asset_id uuid NOT NULL REFERENCES assets(id),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 64),
    document jsonb NOT NULL,
    revision bigint NOT NULL DEFAULT 0 CHECK (revision >= 0),
    active_layer_set_id uuid,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner_user_id, source_asset_id)
);
CREATE INDEX image_editor_projects_owner_updated_idx
    ON image_editor_projects(owner_user_id, updated_at DESC, id DESC);

CREATE TABLE asset_operations (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    editor_project_id uuid NOT NULL REFERENCES image_editor_projects(id) ON DELETE CASCADE,
    operation_type text NOT NULL CHECK (operation_type IN ('layer_decomposition','editor_publish','layer_package')),
    status text NOT NULL DEFAULT 'queued' CHECK (status IN (
        'queued','dispatched','snapshotting','submitting','provider_processing',
        'ingesting','submission_uncertain','succeeded','failed','cancelled'
    )),
    source_revision bigint NOT NULL CHECK (source_revision >= 0),
    source_document jsonb NOT NULL,
    snapshot_asset_id uuid REFERENCES assets(id),
    result_asset_id uuid REFERENCES assets(id),
    layer_set_id uuid,
    model_id text,
    capability_revision text,
    prompt text,
    resolution text,
    prompt_optimization_mode text,
    request_hash text NOT NULL,
    idempotency_key text NOT NULL,
    provider_id text REFERENCES providers(id),
    provider_model text,
    provider_request_id text,
    staged_manifest jsonb,
    usage jsonb NOT NULL DEFAULT '{}'::jsonb,
    error_code text,
    error_message text,
    submission_uncertain boolean NOT NULL DEFAULT false,
    retry_count integer NOT NULL DEFAULT 0 CHECK (retry_count >= 0),
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    river_job_id bigint,
    dispatch_state text NOT NULL DEFAULT 'pending' CHECK (dispatch_state IN ('pending','dispatched','finished')),
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner_user_id, idempotency_key)
);
CREATE INDEX asset_operations_dispatch_idx
    ON asset_operations(next_attempt_at, created_at)
    WHERE dispatch_state='pending';
CREATE INDEX asset_operations_owner_active_idx
    ON asset_operations(owner_user_id, status)
    WHERE status NOT IN ('succeeded','failed','cancelled','submission_uncertain');
CREATE INDEX asset_operations_project_created_idx
    ON asset_operations(editor_project_id, created_at DESC);

CREATE TABLE layer_sets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    editor_project_id uuid NOT NULL REFERENCES image_editor_projects(id) ON DELETE CASCADE,
    asset_operation_id uuid NOT NULL UNIQUE REFERENCES asset_operations(id) ON DELETE CASCADE,
    source_revision bigint NOT NULL CHECK (source_revision >= 0),
    base_asset_id uuid NOT NULL REFERENCES assets(id),
    package_asset_id uuid REFERENCES assets(id),
    package_ready_at timestamptz,
    CHECK ((package_asset_id IS NULL AND package_ready_at IS NULL)
        OR (package_asset_id IS NOT NULL AND package_ready_at IS NOT NULL)),
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX layer_sets_project_created_idx
    ON layer_sets(editor_project_id, created_at DESC);

CREATE TABLE layer_set_items (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    layer_set_id uuid NOT NULL REFERENCES layer_sets(id) ON DELETE CASCADE,
    asset_id uuid NOT NULL REFERENCES assets(id),
    z_index smallint NOT NULL CHECK (z_index BETWEEN 1 AND 16),
    name text NOT NULL CHECK (char_length(name) BETWEEN 1 AND 128),
    description text CHECK (description IS NULL OR char_length(description) <= 1024),
    published_asset_id uuid REFERENCES assets(id),
    bbox_absolute integer[] NOT NULL CHECK (array_length(bbox_absolute,1)=4),
    bbox_normalized double precision[] NOT NULL CHECK (array_length(bbox_normalized,1)=4),
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (layer_set_id, z_index),
    UNIQUE (layer_set_id, asset_id)
);

ALTER TABLE image_editor_projects
    ADD CONSTRAINT image_editor_projects_active_layer_set_fk
    FOREIGN KEY (active_layer_set_id) REFERENCES layer_sets(id) ON DELETE SET NULL;
ALTER TABLE asset_operations
    ADD CONSTRAINT asset_operations_layer_set_fk
    FOREIGN KEY (layer_set_id) REFERENCES layer_sets(id) ON DELETE SET NULL;

ALTER TABLE provider_attempts ALTER COLUMN job_id DROP NOT NULL;
ALTER TABLE provider_attempts ADD COLUMN asset_operation_id uuid REFERENCES asset_operations(id) ON DELETE CASCADE;
ALTER TABLE provider_attempts DROP CONSTRAINT provider_attempts_job_id_operation_attempt_no_key;
ALTER TABLE provider_attempts ADD CONSTRAINT provider_attempts_owner_check
    CHECK (num_nonnulls(job_id, asset_operation_id) = 1);
CREATE UNIQUE INDEX provider_attempts_generation_operation_attempt_idx
    ON provider_attempts(job_id, operation, attempt_no) WHERE job_id IS NOT NULL;
CREATE UNIQUE INDEX provider_attempts_asset_operation_attempt_idx
    ON provider_attempts(asset_operation_id, operation, attempt_no) WHERE asset_operation_id IS NOT NULL;

ALTER TABLE job_events ADD COLUMN asset_operation_id uuid REFERENCES asset_operations(id) ON DELETE CASCADE;
ALTER TABLE job_events ADD COLUMN editor_project_id uuid REFERENCES image_editor_projects(id) ON DELETE CASCADE;
CREATE INDEX job_events_asset_operation_idx ON job_events(asset_operation_id, id) WHERE asset_operation_id IS NOT NULL;

GRANT SELECT, INSERT, UPDATE, DELETE ON image_editor_projects TO studio_api;
GRANT SELECT, INSERT, UPDATE ON asset_operations TO studio_api;
GRANT SELECT ON layer_sets, layer_set_items TO studio_api;
GRANT SELECT, UPDATE, DELETE ON image_editor_projects TO studio_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON asset_operations, layer_sets, layer_set_items TO studio_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON assets TO studio_worker;

-- +goose Down
REVOKE ALL ON image_editor_projects, asset_operations, layer_sets, layer_set_items FROM studio_worker, studio_api;
DROP INDEX IF EXISTS job_events_asset_operation_idx;
ALTER TABLE job_events DROP COLUMN IF EXISTS editor_project_id;
ALTER TABLE job_events DROP COLUMN IF EXISTS asset_operation_id;
DROP INDEX IF EXISTS provider_attempts_asset_operation_attempt_idx;
DROP INDEX IF EXISTS provider_attempts_generation_operation_attempt_idx;
ALTER TABLE provider_attempts DROP CONSTRAINT IF EXISTS provider_attempts_owner_check;
ALTER TABLE provider_attempts DROP COLUMN IF EXISTS asset_operation_id;
ALTER TABLE provider_attempts ALTER COLUMN job_id SET NOT NULL;
ALTER TABLE provider_attempts ADD CONSTRAINT provider_attempts_job_id_operation_attempt_no_key UNIQUE(job_id,operation,attempt_no);
ALTER TABLE image_editor_projects DROP CONSTRAINT IF EXISTS image_editor_projects_active_layer_set_fk;
ALTER TABLE asset_operations DROP CONSTRAINT IF EXISTS asset_operations_layer_set_fk;
DROP TABLE layer_set_items;
DROP TABLE layer_sets;
DROP TABLE asset_operations;
DROP TABLE image_editor_projects;
DELETE FROM assets WHERE kind='derived';
UPDATE assets SET kind='generation' WHERE kind='editor';
ALTER TABLE assets DROP CONSTRAINT assets_kind_check;
ALTER TABLE assets ADD CONSTRAINT assets_kind_check CHECK (kind IN ('upload','generation'));
