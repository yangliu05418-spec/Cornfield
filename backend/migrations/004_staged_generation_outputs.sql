-- +goose Up
CREATE TABLE generation_staged_outputs (
    job_id uuid NOT NULL REFERENCES generation_jobs(id) ON DELETE CASCADE,
    output_index smallint NOT NULL CHECK (output_index >= 0),
    storage_key text NOT NULL,
    sha256 text NOT NULL CHECK (char_length(sha256) = 64),
    media_type text NOT NULL,
    width integer NOT NULL CHECK (width > 0),
    height integer NOT NULL CHECK (height > 0),
    byte_size bigint NOT NULL CHECK (byte_size > 0),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (job_id, output_index)
);

-- +goose Down
DROP TABLE IF EXISTS generation_staged_outputs;
