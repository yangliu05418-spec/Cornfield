-- +goose Up
ALTER TABLE generation_jobs
    ADD COLUMN execution_generation integer NOT NULL DEFAULT 1
    CHECK (execution_generation > 0);

-- +goose Down
ALTER TABLE generation_jobs DROP COLUMN IF EXISTS execution_generation;
