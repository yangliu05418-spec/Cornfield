-- +goose Up
ALTER TABLE generation_batches ADD COLUMN request_hash text;
UPDATE generation_batches SET request_hash = 'legacy:' || id::text WHERE request_hash IS NULL;
ALTER TABLE generation_batches ALTER COLUMN request_hash SET NOT NULL;

ALTER TABLE generation_jobs ADD COLUMN generation_deadline timestamptz;
CREATE INDEX generation_jobs_provider_inflight_idx
    ON generation_jobs (status, batch_id)
    WHERE status IN ('submitting', 'provider_pending', 'ingesting');

CREATE TABLE generation_rate_limits (
    owner_user_id uuid PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
    tokens double precision NOT NULL CHECK (tokens >= 0 AND tokens <= 4),
    updated_at timestamptz NOT NULL
);

-- +goose Down
DROP TABLE IF EXISTS generation_rate_limits;
DROP INDEX IF EXISTS generation_jobs_provider_inflight_idx;
ALTER TABLE generation_jobs DROP COLUMN IF EXISTS generation_deadline;
ALTER TABLE generation_batches DROP COLUMN IF EXISTS request_hash;
