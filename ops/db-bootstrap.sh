#!/bin/sh
set -eu
umask 077

fail() {
  echo "db-bootstrap: $*" >&2
  exit 1
}

read_secret() {
  path="$1"
  test -r "${path}" || fail "cannot read ${path}"
  awk 'NR == 1 && length($0) >= 32 && $0 !~ /[[:space:]]/ { valid=1 } END { exit !(NR == 1 && valid) }' \
    "${path}" || fail "${path} must contain one password of at least 32 characters without whitespace"
  value="$(cat "${path}")"
  printf '%s' "${value}"
}

export PGPASSWORD="$(read_secret /run/secrets/postgres_bootstrap_password)"
export STUDIO_OWNER_PASSWORD="$(read_secret /run/secrets/postgres_owner_password)"
export STUDIO_API_PASSWORD="$(read_secret /run/secrets/postgres_api_password)"
export STUDIO_WORKER_PASSWORD="$(read_secret /run/secrets/postgres_worker_password)"

bootstrap_role=studio_bootstrap
if ! psql -X --no-password --host=postgres --port=5432 --username="${bootstrap_role}" --dbname=studio --quiet --tuples-only --command='SELECT 1' >/dev/null 2>&1; then
  # One-time bridge for volumes created by the original Compose file, where
  # `studio` was both the init superuser and the application login.
  bootstrap_role=studio
  psql -X --no-password --host=postgres --port=5432 --username="${bootstrap_role}" --dbname=studio --quiet --tuples-only --command='SELECT 1' >/dev/null 2>&1 || \
    fail "neither studio_bootstrap nor the legacy studio bootstrap role accepted the configured password"
fi

psql -X --quiet --no-password --set=ON_ERROR_STOP=1 --host=postgres --port=5432 --username="${bootstrap_role}" --dbname=studio <<'SQL'
\getenv bootstrap_password PGPASSWORD
\getenv owner_password STUDIO_OWNER_PASSWORD
\getenv api_password STUDIO_API_PASSWORD
\getenv worker_password STUDIO_WORKER_PASSWORD

SELECT format('CREATE ROLE studio_bootstrap LOGIN SUPERUSER CREATEDB CREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', :'bootstrap_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'studio_bootstrap') \gexec
SELECT format('ALTER ROLE studio_bootstrap LOGIN SUPERUSER CREATEDB CREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', :'bootstrap_password') \gexec

SELECT format('CREATE ROLE studio_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', :'owner_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'studio_owner') \gexec
SELECT format('CREATE ROLE studio_api LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', :'api_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'studio_api') \gexec
SELECT format('CREATE ROLE studio_worker LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', :'worker_password')
WHERE NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'studio_worker') \gexec

SELECT format('ALTER ROLE studio_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', :'owner_password') \gexec
SELECT format('ALTER ROLE studio_api LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', :'api_password') \gexec
SELECT format('ALTER ROLE studio_worker LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD %L', :'worker_password') \gexec

-- Preserve upgrades from the original single-role deployment: the bootstrap
-- role remains cluster-local, while public application objects become owned
-- by the non-superuser migration role. Extension members are deliberately
-- skipped: their ownership is managed by PostgreSQL as an extension unit.
DO $$
DECLARE
    object_record record;
BEGIN
    FOR object_record IN
        SELECT n.nspname, c.relname, c.relkind
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        JOIN pg_roles owner_role ON owner_role.oid = c.relowner
        WHERE n.nspname = 'public'
          AND owner_role.rolname IN ('studio', 'studio_bootstrap')
          AND c.relkind IN ('r', 'p', 'S', 'v', 'm', 'f')
          AND NOT EXISTS (
              SELECT 1 FROM pg_depend d
              WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e'
          )
    LOOP
        EXECUTE format(
            'ALTER %s %I.%I OWNER TO studio_owner',
            CASE object_record.relkind
                WHEN 'S' THEN 'SEQUENCE'
                WHEN 'v' THEN 'VIEW'
                WHEN 'm' THEN 'MATERIALIZED VIEW'
                WHEN 'f' THEN 'FOREIGN TABLE'
                ELSE 'TABLE'
            END,
            object_record.nspname,
            object_record.relname
        );
    END LOOP;

    FOR object_record IN
        SELECT n.nspname, p.proname, p.prokind, pg_get_function_identity_arguments(p.oid) AS arguments
        FROM pg_proc p
        JOIN pg_namespace n ON n.oid = p.pronamespace
        JOIN pg_roles owner_role ON owner_role.oid = p.proowner
        WHERE n.nspname = 'public'
          AND owner_role.rolname IN ('studio', 'studio_bootstrap')
          AND NOT EXISTS (
              SELECT 1 FROM pg_depend d
              WHERE d.classid = 'pg_proc'::regclass AND d.objid = p.oid AND d.deptype = 'e'
          )
    LOOP
        EXECUTE format(
            'ALTER %s %I.%I(%s) OWNER TO studio_owner',
            CASE WHEN object_record.prokind = 'p' THEN 'PROCEDURE' ELSE 'FUNCTION' END,
            object_record.nspname,
            object_record.proname,
            object_record.arguments
        );
    END LOOP;

    FOR object_record IN
        SELECT n.nspname, t.typname
        FROM pg_type t
        JOIN pg_namespace n ON n.oid = t.typnamespace
        JOIN pg_roles owner_role ON owner_role.oid = t.typowner
        WHERE n.nspname = 'public'
          AND owner_role.rolname IN ('studio', 'studio_bootstrap')
          AND t.typtype IN ('d', 'e')
          AND NOT EXISTS (
              SELECT 1 FROM pg_depend d
              WHERE d.classid = 'pg_type'::regclass AND d.objid = t.oid AND d.deptype = 'e'
          )
    LOOP
        EXECUTE format('ALTER TYPE %I.%I OWNER TO studio_owner', object_record.nspname, object_record.typname);
    END LOOP;
END
$$;

ALTER DATABASE studio OWNER TO studio_owner;
ALTER SCHEMA public OWNER TO studio_owner;

REVOKE ALL ON DATABASE studio FROM PUBLIC;
GRANT CONNECT ON DATABASE studio TO studio_owner, studio_api, studio_worker;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO studio_api, studio_worker;

-- The legacy login is disabled only after its application objects have moved.
-- `studio_bootstrap` is the sole retained cluster bootstrap superuser and is
-- never mounted into API or Worker containers.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'studio') THEN
        EXECUTE 'ALTER ROLE studio NOLOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS';
    END IF;
END
$$;
SQL

unset PGPASSWORD STUDIO_OWNER_PASSWORD STUDIO_API_PASSWORD STUDIO_WORKER_PASSWORD
echo "db-bootstrap: roles and ownership are current"
