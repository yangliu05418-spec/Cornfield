CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE INDEX IF NOT EXISTS assets_original_filename_trgm_idx
    ON assets USING gin (lower(original_filename) gin_trgm_ops)
    WHERE original_filename IS NOT NULL AND purged_at IS NULL;

CREATE INDEX IF NOT EXISTS generation_batches_prompt_trgm_idx
    ON generation_batches USING gin (lower(prompt) gin_trgm_ops);
