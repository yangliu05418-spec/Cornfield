-- +goose Up
-- PostgreSQL requires UPDATE privilege on at least one column for every
-- SELECT locking clause. The API uses FOR SHARE while validating reference
-- assets so retention cannot purge them before generation_input_assets is
-- committed. Give it a constrained, non-business column rather than UPDATE
-- access to storage, ownership, expiry, or purge state.
ALTER TABLE assets
    ADD COLUMN lock_guard boolean NOT NULL DEFAULT false CHECK (lock_guard = false);
GRANT UPDATE (lock_guard) ON assets TO studio_api;

-- +goose Down
REVOKE UPDATE (lock_guard) ON assets FROM studio_api;
ALTER TABLE assets DROP COLUMN lock_guard;
