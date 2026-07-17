-- +goose Up
-- API permissions are intentionally table-specific. New application tables
-- must grant the API only the operations used by their handlers.
GRANT SELECT, INSERT, UPDATE ON users, user_sessions TO studio_api;
GRANT INSERT ON audit_logs TO studio_api;
GRANT SELECT ON providers, models, model_capability_versions, provider_attempts,
    assets, generation_outputs, service_heartbeats TO studio_api;
GRANT SELECT, INSERT, UPDATE ON generation_batches, generation_jobs,
    upload_sessions, generation_rate_limits TO studio_api;
GRANT SELECT, INSERT ON generation_input_assets, job_events,
    provider_callback_events TO studio_api;
GRANT USAGE ON SEQUENCE audit_logs_id_seq, job_events_id_seq,
    provider_callback_events_id_seq TO studio_api;

-- The Worker owns the generation lifecycle and retention maintenance, but has
-- no access to users beyond deleting expired session rows.
GRANT DELETE ON user_sessions TO studio_worker;
GRANT SELECT (expires_at, revoked_at) ON user_sessions TO studio_worker;
GRANT SELECT ON model_capability_versions, generation_input_assets TO studio_worker;
GRANT SELECT, UPDATE ON providers, upload_sessions TO studio_worker;
GRANT SELECT, UPDATE ON generation_jobs TO studio_worker;
GRANT SELECT, UPDATE, DELETE ON generation_batches TO studio_worker;
GRANT SELECT, INSERT ON generation_outputs TO studio_worker;
GRANT SELECT, INSERT, DELETE ON job_events, provider_attempts TO studio_worker;
GRANT SELECT, INSERT, UPDATE ON service_heartbeats TO studio_worker;
GRANT SELECT, INSERT, UPDATE, DELETE ON generation_staged_outputs TO studio_worker;
GRANT SELECT, INSERT, UPDATE ON assets TO studio_worker;
GRANT SELECT, UPDATE, DELETE ON provider_callback_events TO studio_worker;
GRANT USAGE ON SEQUENCE job_events_id_seq, provider_attempts_id_seq TO studio_worker;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION grant_studio_worker_river_privileges() RETURNS void
LANGUAGE plpgsql
AS $$
DECLARE
    relation_name text;
BEGIN
    FOR relation_name IN
        SELECT format('%I.%I', n.nspname, c.relname)
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relkind IN ('r', 'p')
          AND left(c.relname, 6) = 'river_'
    LOOP
        IF relation_name = 'public.river_migration' THEN
            EXECUTE format('GRANT SELECT ON TABLE %s TO studio_worker', relation_name);
        ELSE
            EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %s TO studio_worker', relation_name);
        END IF;
    END LOOP;
    FOR relation_name IN
        SELECT format('%I.%I', n.nspname, c.relname)
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relkind = 'S'
          AND left(c.relname, 6) = 'river_'
    LOOP
        EXECUTE format('GRANT USAGE, SELECT ON SEQUENCE %s TO studio_worker', relation_name);
    END LOOP;
END
$$;
-- +goose StatementEnd
REVOKE ALL ON FUNCTION grant_studio_worker_river_privileges() FROM PUBLIC;
SELECT grant_studio_worker_river_privileges();

-- +goose Down
REVOKE ALL ON users, user_sessions, audit_logs, providers, models,
    model_capability_versions, generation_batches, generation_jobs,
    provider_attempts, assets, generation_input_assets, generation_outputs,
    upload_sessions, job_events, provider_callback_events,
    generation_rate_limits, service_heartbeats, generation_staged_outputs
    FROM studio_api, studio_worker;
REVOKE SELECT (expires_at, revoked_at) ON user_sessions FROM studio_worker;
REVOKE ALL ON SEQUENCE audit_logs_id_seq, job_events_id_seq,
    provider_attempts_id_seq, provider_callback_events_id_seq
    FROM studio_api, studio_worker;

-- +goose StatementBegin
DO $$
DECLARE
    object_record record;
BEGIN
    FOR object_record IN
        SELECT format('%I.%I', n.nspname, c.relname) AS relation_name, c.relkind
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname = 'public'
          AND c.relkind IN ('r', 'p', 'S')
          AND left(c.relname, 6) = 'river_'
    LOOP
        EXECUTE format(
            'REVOKE ALL ON %s %s FROM studio_worker',
            CASE WHEN object_record.relkind = 'S' THEN 'SEQUENCE' ELSE 'TABLE' END,
            object_record.relation_name
        );
    END LOOP;
END
$$;
-- +goose StatementEnd
DROP FUNCTION IF EXISTS grant_studio_worker_river_privileges();
