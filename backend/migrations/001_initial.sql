-- +goose Up
CREATE EXTENSION IF NOT EXISTS pgcrypto;
CREATE EXTENSION IF NOT EXISTS citext;

CREATE TABLE users (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username citext NOT NULL UNIQUE,
    display_name text NOT NULL,
    password_hash text NOT NULL,
    role text NOT NULL DEFAULT 'member' CHECK (role IN ('member', 'admin')),
    status text NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
    must_change_password boolean NOT NULL DEFAULT true,
    temporary_password_expires_at timestamptz,
    session_version integer NOT NULL DEFAULT 1,
    last_login_at timestamptz,
    created_by uuid REFERENCES users(id),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE user_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash bytea NOT NULL UNIQUE,
    csrf_hash bytea NOT NULL,
    session_version integer NOT NULL,
    user_agent text NOT NULL DEFAULT '',
    ip_hash bytea,
    expires_at timestamptz NOT NULL,
    idle_expires_at timestamptz NOT NULL,
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_sessions_active_idx ON user_sessions (user_id, idle_expires_at) WHERE revoked_at IS NULL;

CREATE TABLE audit_logs (
    id bigserial PRIMARY KEY,
    actor_user_id uuid REFERENCES users(id),
    action text NOT NULL,
    target_type text NOT NULL,
    target_id text,
    request_id text,
    metadata jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE providers (
    id text PRIMARY KEY,
    display_name text NOT NULL,
    enabled boolean NOT NULL DEFAULT true,
    state text NOT NULL DEFAULT 'unknown' CHECK (state IN ('unknown', 'healthy', 'degraded', 'paused')),
    breaker_open_until timestamptz,
    last_probe_at timestamptz,
    last_error_code text,
    last_error_at timestamptz,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE models (
    id text PRIMARY KEY,
    provider_id text NOT NULL REFERENCES providers(id),
    provider_model text NOT NULL,
    display_name text NOT NULL,
    enabled boolean NOT NULL,
    sort_order integer NOT NULL DEFAULT 0,
    current_revision text NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE model_capability_versions (
    model_id text NOT NULL REFERENCES models(id),
    revision text NOT NULL,
    config jsonb NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (model_id, revision)
);

CREATE TABLE generation_batches (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    idempotency_key text NOT NULL,
    model_id text NOT NULL REFERENCES models(id),
    capability_revision text NOT NULL,
    prompt text NOT NULL CHECK (char_length(prompt) BETWEEN 1 AND 8192),
    aspect_ratio text NOT NULL,
    resolution text NOT NULL,
    draw_count smallint NOT NULL CHECK (draw_count BETWEEN 1 AND 4),
    expected_outputs smallint NOT NULL CHECK (expected_outputs BETWEEN 1 AND 16),
    completed_outputs smallint NOT NULL DEFAULT 0,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','running','partial','succeeded','failed','cancelling','cancelled')),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (owner_user_id, idempotency_key)
);
CREATE INDEX generation_batches_owner_cursor_idx ON generation_batches (owner_user_id, created_at DESC, id DESC);

CREATE TABLE generation_jobs (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    batch_id uuid NOT NULL REFERENCES generation_batches(id) ON DELETE CASCADE,
    owner_user_id uuid NOT NULL REFERENCES users(id),
    draw_index smallint NOT NULL,
    status text NOT NULL DEFAULT 'queued' CHECK (status IN ('queued','dispatched','submitting','submission_uncertain','provider_pending','ingesting','succeeded','failed','cancelling','cancelled')),
    dispatch_state text NOT NULL DEFAULT 'pending' CHECK (dispatch_state IN ('pending','dispatched','finished')),
    river_job_id bigint,
    provider_job_id text,
    expected_outputs smallint NOT NULL,
    attempt_count integer NOT NULL DEFAULT 0,
    retryable boolean NOT NULL DEFAULT true,
    cancel_mode text CHECK (cancel_mode IN ('local','requested_upstream','discard_result_only')),
    error_code text,
    error_message text,
    submission_uncertain boolean NOT NULL DEFAULT false,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    dispatched_at timestamptz,
    started_at timestamptz,
    completed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (batch_id, draw_index)
);
CREATE INDEX generation_jobs_dispatch_idx ON generation_jobs (next_attempt_at, created_at) WHERE dispatch_state = 'pending';
CREATE INDEX generation_jobs_owner_active_idx ON generation_jobs (owner_user_id, status) WHERE status NOT IN ('succeeded','failed','cancelled');

CREATE TABLE provider_attempts (
    id bigserial PRIMARY KEY,
    job_id uuid NOT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE,
    provider_id text NOT NULL REFERENCES providers(id),
    operation text NOT NULL,
    attempt_no integer NOT NULL,
    provider_request_id text,
    http_status integer,
    duration_ms integer,
    outcome text NOT NULL,
    error_code text,
    error_message text,
    usage jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (job_id, operation, attempt_no)
);

CREATE TABLE assets (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    kind text NOT NULL CHECK (kind IN ('upload','generation')),
    storage_key text NOT NULL,
    sha256 text NOT NULL,
    media_type text NOT NULL,
    original_filename text,
    width integer NOT NULL CHECK (width > 0),
    height integer NOT NULL CHECK (height > 0),
    byte_size bigint NOT NULL CHECK (byte_size > 0),
    blur_data_url text,
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '90 days'),
    purge_pending boolean NOT NULL DEFAULT false,
    purged_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX assets_owner_cursor_idx ON assets (owner_user_id, created_at DESC, id DESC) WHERE purged_at IS NULL;
CREATE INDEX assets_expiry_idx ON assets (expires_at) WHERE purged_at IS NULL;
CREATE INDEX assets_sha_idx ON assets (owner_user_id, sha256) WHERE purged_at IS NULL;
CREATE INDEX assets_storage_key_idx ON assets (storage_key);

CREATE TABLE generation_input_assets (
    batch_id uuid NOT NULL REFERENCES generation_batches(id) ON DELETE CASCADE,
    asset_id uuid NOT NULL REFERENCES assets(id),
    position smallint NOT NULL,
    PRIMARY KEY (batch_id, asset_id)
);

CREATE TABLE generation_outputs (
    job_id uuid NOT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE,
    asset_id uuid NOT NULL REFERENCES assets(id),
    output_index smallint NOT NULL,
    PRIMARY KEY (job_id, output_index),
    UNIQUE (asset_id)
);

CREATE TABLE upload_sessions (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    status text NOT NULL DEFAULT 'created' CHECK (status IN ('created','uploading','validating','ready','failed','expired')),
    original_filename text NOT NULL,
    declared_media_type text NOT NULL,
    declared_size bigint NOT NULL CHECK (declared_size BETWEEN 1 AND 26214400),
    quarantine_key text,
    asset_id uuid REFERENCES assets(id),
    error_code text,
    expires_at timestamptz NOT NULL DEFAULT (now() + interval '1 hour'),
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE job_events (
    id bigserial PRIMARY KEY,
    owner_user_id uuid NOT NULL REFERENCES users(id),
    batch_id uuid REFERENCES generation_batches(id) ON DELETE CASCADE,
    job_id uuid REFERENCES generation_jobs(id) ON DELETE CASCADE,
    event_type text NOT NULL,
    payload jsonb NOT NULL DEFAULT '{}'::jsonb,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX job_events_owner_cursor_idx ON job_events (owner_user_id, id);

CREATE TABLE provider_callback_events (
    id bigserial PRIMARY KEY,
    provider_id text NOT NULL REFERENCES providers(id),
    event_hash text NOT NULL,
    provider_job_id text,
    payload jsonb NOT NULL,
    processed_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    UNIQUE (provider_id, event_hash)
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION notify_job_event() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('job_events', NEW.owner_user_id::text);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER job_events_notify AFTER INSERT ON job_events FOR EACH ROW EXECUTE FUNCTION notify_job_event();

INSERT INTO providers (id, display_name) VALUES
    ('legnext', 'Legnext'),
    ('openrouter', 'OpenRouter')
ON CONFLICT (id) DO NOTHING;

-- +goose Down
DROP TRIGGER IF EXISTS job_events_notify ON job_events;
DROP FUNCTION IF EXISTS notify_job_event();
DROP TABLE IF EXISTS provider_callback_events, job_events, upload_sessions, generation_outputs,
    generation_input_assets, assets, provider_attempts, generation_jobs, generation_batches,
    model_capability_versions, models, providers, audit_logs, user_sessions, users CASCADE;
