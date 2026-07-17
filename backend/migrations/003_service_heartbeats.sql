-- +goose Up
CREATE TABLE service_heartbeats (
    service_name text NOT NULL,
    instance_id text NOT NULL,
    started_at timestamptz NOT NULL DEFAULT now(),
    heartbeat_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (service_name, instance_id)
);

CREATE INDEX service_heartbeats_freshness_idx
    ON service_heartbeats (service_name, heartbeat_at DESC);

-- +goose Down
DROP TABLE IF EXISTS service_heartbeats;
