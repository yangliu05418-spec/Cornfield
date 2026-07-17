-- +goose Up
-- The API may only clear runtime health fields through the authenticated,
-- audited provider-resume handler. Static provider configuration remains
-- owned by migrations/modelctl.
GRANT UPDATE (state, breaker_open_until, last_error_code, last_error_at, updated_at)
    ON providers TO studio_api;

-- +goose Down
REVOKE UPDATE (state, breaker_open_until, last_error_code, last_error_at, updated_at)
    ON providers FROM studio_api;
