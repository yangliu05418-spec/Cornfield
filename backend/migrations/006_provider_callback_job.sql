-- +goose Up
ALTER TABLE provider_callback_events
    ADD COLUMN generation_job_id uuid REFERENCES generation_jobs(id) ON DELETE CASCADE;

-- Historical callbacks may contain arbitrary provider JSON. Retain only the
-- normalized status needed for support/audit and make regressions fail closed.
UPDATE provider_callback_events
SET payload = jsonb_build_object(
    'status',
    CASE lower(COALESCE(payload->>'status', ''))
        WHEN 'queued' THEN 'queued'
        WHEN 'pending' THEN 'pending'
        WHEN 'processing' THEN 'processing'
        WHEN 'running' THEN 'running'
        WHEN 'completed' THEN 'completed'
        WHEN 'succeeded' THEN 'succeeded'
        WHEN 'failed' THEN 'failed'
        WHEN 'canceled' THEN 'canceled'
        WHEN 'cancelled' THEN 'cancelled'
        ELSE 'unknown'
    END
);

ALTER TABLE provider_callback_events
    ADD CONSTRAINT provider_callback_payload_minimal CHECK (
        jsonb_typeof(payload) = 'object'
        AND payload = jsonb_build_object('status', payload->>'status')
        AND payload->>'status' IS NOT NULL
        AND jsonb_typeof(payload->'status') = 'string'
        AND payload->>'status' IN (
            'queued','pending','processing','running','completed',
            'succeeded','failed','canceled','cancelled','unknown'
        )
    );

CREATE INDEX provider_callback_events_job_idx
    ON provider_callback_events (generation_job_id, created_at)
    WHERE generation_job_id IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS provider_callback_events_job_idx;
ALTER TABLE provider_callback_events DROP CONSTRAINT IF EXISTS provider_callback_payload_minimal;
ALTER TABLE provider_callback_events DROP COLUMN IF EXISTS generation_job_id;
