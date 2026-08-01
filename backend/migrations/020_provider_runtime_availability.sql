-- +goose Up
ALTER TABLE providers
    ADD COLUMN last_probe_state text NOT NULL DEFAULT 'unknown'
        CHECK (last_probe_state IN ('unknown','healthy','degraded','paused')),
    ADD COLUMN last_probe_error_code text;

-- The Worker records probe observations even while the provider remains
-- administratively paused. The API only reads these fields when deciding
-- whether an audited resume is safe.
GRANT UPDATE (last_probe_state, last_probe_error_code) ON providers TO studio_worker;

-- +goose Down
REVOKE UPDATE (last_probe_state, last_probe_error_code) ON providers FROM studio_worker;
ALTER TABLE providers
    DROP COLUMN IF EXISTS last_probe_error_code,
    DROP COLUMN IF EXISTS last_probe_state;
