-- +goose Up
-- A cancelled or submission-uncertain business job can remain active (and
-- billable) at a provider. Keep that remote occupancy durable without making
-- the user-visible job non-terminal. A queued row is allowed only as the short
-- durable hand-off after an administrator attaches a verified remote job ID.
ALTER TABLE generation_jobs ADD COLUMN upstream_active_until timestamptz;
ALTER TABLE generation_jobs ADD CONSTRAINT generation_jobs_upstream_active_status_chk
    CHECK (
        upstream_active_until IS NULL
        OR status IN ('cancelled', 'failed', 'submission_uncertain')
        OR (status = 'queued' AND provider_job_id IS NOT NULL)
    );
CREATE INDEX generation_jobs_upstream_active_idx
    ON generation_jobs (upstream_active_until)
    WHERE upstream_active_until IS NOT NULL;

-- +goose Down
DROP INDEX IF EXISTS generation_jobs_upstream_active_idx;
ALTER TABLE generation_jobs DROP CONSTRAINT IF EXISTS generation_jobs_upstream_active_status_chk;
ALTER TABLE generation_jobs DROP COLUMN IF EXISTS upstream_active_until;
