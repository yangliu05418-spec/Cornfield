-- +goose Up
ALTER TABLE generation_batches
    ADD COLUMN options jsonb NOT NULL DEFAULT '{}'::jsonb;

ALTER TABLE generation_outputs
    ADD COLUMN deleted_at timestamptz;

ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check
    CHECK (status IN ('active', 'disabled', 'deleting', 'deleted'));

CREATE TABLE deletion_requests (
    id uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    kind text NOT NULL CHECK (kind IN ('asset', 'user')),
    owner_user_id uuid NOT NULL REFERENCES users(id),
    asset_id uuid REFERENCES assets(id),
    target_user_id uuid REFERENCES users(id),
    requested_by uuid NOT NULL REFERENCES users(id),
    status text NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'running', 'succeeded', 'failed')),
    attempt_count integer NOT NULL DEFAULT 0,
    next_attempt_at timestamptz NOT NULL DEFAULT now(),
    error_code text,
    error_message text,
    created_at timestamptz NOT NULL DEFAULT now(),
    started_at timestamptz,
    completed_at timestamptz,
    CHECK ((kind='asset' AND asset_id IS NOT NULL AND target_user_id IS NULL)
        OR (kind='user' AND target_user_id IS NOT NULL AND asset_id IS NULL))
);
CREATE UNIQUE INDEX deletion_requests_active_asset_idx ON deletion_requests(asset_id)
    WHERE kind='asset' AND status IN ('pending','running');
CREATE UNIQUE INDEX deletion_requests_active_user_idx ON deletion_requests(target_user_id)
    WHERE kind='user' AND status IN ('pending','running');
CREATE INDEX deletion_requests_pending_idx ON deletion_requests(next_attempt_at,created_at,id)
    WHERE status='pending';

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION notify_deletion_request() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    PERFORM pg_notify('deletion_requests', NEW.id::text);
    RETURN NEW;
END;
$$;
-- +goose StatementEnd
CREATE TRIGGER deletion_requests_notify AFTER INSERT ON deletion_requests
    FOR EACH ROW EXECUTE FUNCTION notify_deletion_request();

GRANT SELECT, INSERT ON deletion_requests TO studio_api;
GRANT SELECT, UPDATE ON deletion_requests TO studio_worker;
GRANT UPDATE (purge_pending) ON assets TO studio_api;
GRANT UPDATE (deleted_at) ON generation_outputs TO studio_api;
GRANT UPDATE (status, session_version, updated_at) ON users TO studio_api;
GRANT DELETE ON upload_sessions, generation_rate_limits TO studio_worker;
GRANT DELETE ON assets TO studio_worker;
GRANT UPDATE (metadata) ON audit_logs TO studio_worker;
GRANT UPDATE (username, display_name, password_hash, status, must_change_password,
    temporary_password_expires_at, updated_at) ON users TO studio_worker;

-- +goose Down
REVOKE ALL ON deletion_requests FROM studio_api, studio_worker;
REVOKE UPDATE (purge_pending) ON assets FROM studio_api;
REVOKE UPDATE (deleted_at) ON generation_outputs FROM studio_api;
REVOKE DELETE ON upload_sessions, generation_rate_limits FROM studio_worker;
REVOKE DELETE ON assets FROM studio_worker;
REVOKE UPDATE (metadata) ON audit_logs FROM studio_worker;
REVOKE UPDATE (username, display_name, password_hash, status, must_change_password,
    temporary_password_expires_at, updated_at) ON users FROM studio_worker;
DROP TRIGGER IF EXISTS deletion_requests_notify ON deletion_requests;
DROP FUNCTION IF EXISTS notify_deletion_request();
DROP TABLE IF EXISTS deletion_requests;
ALTER TABLE users DROP CONSTRAINT users_status_check;
ALTER TABLE users ADD CONSTRAINT users_status_check CHECK (status IN ('active', 'disabled'));
ALTER TABLE generation_outputs DROP COLUMN IF EXISTS deleted_at;
ALTER TABLE generation_batches DROP COLUMN IF EXISTS options;
