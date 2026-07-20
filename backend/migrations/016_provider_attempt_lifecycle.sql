-- +goose Up
ALTER TABLE provider_attempts ADD COLUMN finished_at timestamptz;

UPDATE provider_attempts
SET finished_at = created_at + (COALESCE(duration_ms, 0)::text || ' milliseconds')::interval
WHERE outcome <> 'started' AND finished_at IS NULL;

CREATE INDEX provider_attempts_unfinished_submit_idx
    ON provider_attempts (created_at, job_id)
    WHERE operation = 'submit' AND outcome = 'started' AND finished_at IS NULL;

GRANT UPDATE ON provider_attempts TO studio_worker;

-- +goose Down
REVOKE UPDATE ON provider_attempts FROM studio_worker;
DROP INDEX IF EXISTS provider_attempts_unfinished_submit_idx;
ALTER TABLE provider_attempts DROP COLUMN IF EXISTS finished_at;
