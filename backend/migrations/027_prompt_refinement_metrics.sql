-- +goose Up
CREATE TABLE prompt_refinement_metrics (
    id uuid PRIMARY KEY,
    model_id text NOT NULL REFERENCES models(id),
    policy_version text NOT NULL,
    outcome text NOT NULL CHECK (outcome IN (
        'optimized','unchanged','provider_error','validation_error'
    )),
    input_runes integer NOT NULL CHECK (input_runes >= 0),
    output_runes integer CHECK (output_runes IS NULL OR output_runes >= 0),
    edit_distance_bucket text CHECK (edit_distance_bucket IS NULL OR edit_distance_bucket IN (
        'none','0_10','10_25','25_45','over_45'
    )),
    latency_ms integer NOT NULL CHECK (latency_ms >= 0),
    changed boolean NOT NULL DEFAULT false,
    prompt_tokens bigint CHECK (prompt_tokens IS NULL OR prompt_tokens >= 0),
    completion_tokens bigint CHECK (completion_tokens IS NULL OR completion_tokens >= 0),
    risk_categories text[] NOT NULL DEFAULT '{}'::text[],
    undone_at timestamptz,
    submitted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX prompt_refinement_metrics_created_idx
    ON prompt_refinement_metrics(created_at);

GRANT SELECT, INSERT, UPDATE ON prompt_refinement_metrics TO studio_api;
GRANT SELECT, DELETE ON prompt_refinement_metrics TO studio_worker;

-- +goose Down
REVOKE ALL ON prompt_refinement_metrics FROM studio_api, studio_worker;
DROP TABLE prompt_refinement_metrics;
