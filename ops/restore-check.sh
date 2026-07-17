#!/usr/bin/env bash
set -euo pipefail
umask 077

data_root="${DATA_ROOT:?DATA_ROOT must match the production asset directory}"
restore_check_root="${RESTORE_CHECK_ROOT:?RESTORE_CHECK_ROOT must be a dedicated restore filesystem}"
sample_size="${RESTORE_CHECK_SAMPLE_SIZE:-20}"
postgres_image="postgres:18.4-bookworm@sha256:1961f96e6029a02c3812d7cb329a3b03a3ac2bb067058dec17b0f5596aca9296"
check_root=""
container="cornfield-restore-${RANDOM}-$$"
password="$(openssl rand -hex 24)"

cleanup() {
  docker rm -f "${container}" >/dev/null 2>&1 || true
  if test -n "${check_root}" && [[ "${check_root}" == "${restore_check_root}"/cornfield-restore-* ]] && test -d "${check_root}"; then
    rm -rf -- "${check_root}"
  fi
}
trap cleanup EXIT INT TERM

case "${data_root}" in
  /*) ;;
  *) echo "DATA_ROOT must be absolute" >&2; exit 2 ;;
esac
case "${restore_check_root}" in
  /) echo "RESTORE_CHECK_ROOT must not be the filesystem root" >&2; exit 2 ;;
  /*) ;;
  *) echo "RESTORE_CHECK_ROOT must be absolute" >&2; exit 2 ;;
esac
case "${sample_size}" in
  ''|*[!0-9]*) echo "RESTORE_CHECK_SAMPLE_SIZE must be numeric" >&2; exit 2 ;;
esac

test -d "${data_root}"
test -d "${restore_check_root}"
restore_check_root="$(readlink -f -- "${restore_check_root}")"
data_root="$(readlink -f -- "${data_root}")"
case "${restore_check_root}/" in
  "${data_root}/"*) echo "RESTORE_CHECK_ROOT must not be inside DATA_ROOT" >&2; exit 2 ;;
esac
case "${data_root}/" in
  "${restore_check_root}/"*) echo "DATA_ROOT must not be inside RESTORE_CHECK_ROOT" >&2; exit 2 ;;
esac
restore_device="$(stat -c '%d' "${restore_check_root}")"
root_device="$(stat -c '%d' /)"
data_device="$(stat -c '%d' "${data_root}")"
test "${restore_device}" != "${root_device}" || {
  echo "RESTORE_CHECK_ROOT must be on a filesystem separate from /" >&2
  exit 2
}
test "${restore_device}" != "${data_device}" || {
  echo "RESTORE_CHECK_ROOT must be on a filesystem separate from DATA_ROOT" >&2
  exit 2
}

check_root="$(mktemp -d -p "${restore_check_root}" cornfield-restore-XXXXXX)"
restore_target="${check_root}/snapshot"
postgres_data="${check_root}/postgres"
mkdir -p "${restore_target}" "${postgres_data}"

restic restore latest --target "${restore_target}"
dump_file="$(find "${restore_target}" -type f -name 'studio-*.dump' -print | sort | tail -n 1)"
test -n "${dump_file}"
restored_assets="${restore_target}${data_root}/assets"
test -d "${restored_assets}"

docker run -d --rm --name "${container}" --memory 4g --cpus 2 --pids-limit 512 \
  --mount "type=bind,src=${postgres_data},dst=/var/lib/postgresql" \
  -e POSTGRES_DB=studio -e POSTGRES_USER=studio_bootstrap -e POSTGRES_PASSWORD="${password}" \
  "${postgres_image}" >/dev/null

for _ in $(seq 1 60); do
  if docker exec "${container}" pg_isready -U studio_bootstrap -d studio >/dev/null 2>&1; then
    break
  fi
  sleep 1
done
docker exec "${container}" pg_isready -U studio_bootstrap -d studio >/dev/null

# pg_dump archives object ownership and ACLs, but not cluster roles. Recreate
# the same least-privilege role boundary before restoring the archive so the
# exercise fails if an owner or grantee is missing.
docker exec -i "${container}" psql -X --no-password -U studio_bootstrap -d studio -v ON_ERROR_STOP=1 <<'SQL'
CREATE ROLE studio_owner LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE studio_api LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
CREATE ROLE studio_worker LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS;
ALTER DATABASE studio OWNER TO studio_owner;
ALTER SCHEMA public OWNER TO studio_owner;
REVOKE ALL ON DATABASE studio FROM PUBLIC;
GRANT CONNECT ON DATABASE studio TO studio_owner, studio_api, studio_worker;
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO studio_api, studio_worker;
SQL

docker cp "${dump_file}" "${container}:/tmp/studio.dump"
# Restore as the non-superuser object owner. This proves the archive does not
# depend on bootstrap privileges and causes any unexpected owner to fail fast.
docker exec "${container}" pg_restore --no-password -U studio_owner -d studio \
  --exit-on-error --single-transaction /tmp/studio.dump

role_hardening="$(docker exec "${container}" psql -X --no-password -U studio_bootstrap -d studio -Atc \
  "SELECT count(*)=3
      AND bool_and(rolcanlogin AND NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls)
      AND (SELECT rolcanlogin AND rolsuper FROM pg_roles WHERE rolname='studio_bootstrap')
      AND NOT EXISTS (SELECT 1 FROM pg_roles WHERE rolname='studio')
   FROM pg_roles WHERE rolname IN ('studio_owner','studio_api','studio_worker')")"
test "${role_hardening}" = "t"
ownership_hardening="$(docker exec "${container}" psql -X --no-password -U studio_bootstrap -d studio -Atc \
  "SELECT
      (SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname='studio')='studio_owner'
      AND (SELECT pg_get_userbyid(nspowner) FROM pg_namespace WHERE nspname='public')='studio_owner'
      AND NOT EXISTS (
          SELECT 1 FROM pg_class c
          JOIN pg_namespace n ON n.oid=c.relnamespace
          WHERE n.nspname='public' AND c.relkind IN ('r','p','S','v','m','f')
            AND pg_get_userbyid(c.relowner) <> 'studio_owner'
            AND NOT EXISTS (
                SELECT 1 FROM pg_depend d
                WHERE d.classid='pg_class'::regclass AND d.objid=c.oid AND d.deptype='e'
            )
      )
      AND NOT EXISTS (
          SELECT 1 FROM pg_proc p
          JOIN pg_namespace n ON n.oid=p.pronamespace
          WHERE n.nspname='public' AND pg_get_userbyid(p.proowner) <> 'studio_owner'
            AND NOT EXISTS (
                SELECT 1 FROM pg_depend d
                WHERE d.classid='pg_proc'::regclass AND d.objid=p.oid AND d.deptype='e'
            )
      )
      AND NOT EXISTS (
          SELECT 1 FROM pg_type t
          JOIN pg_namespace n ON n.oid=t.typnamespace
          WHERE n.nspname='public' AND t.typtype IN ('d','e')
            AND pg_get_userbyid(t.typowner) <> 'studio_owner'
            AND NOT EXISTS (
                SELECT 1 FROM pg_depend d
                WHERE d.classid='pg_type'::regclass AND d.objid=t.oid AND d.deptype='e'
            )
      )")"
test "${ownership_hardening}" = "t"
runtime_privileges="$(docker exec "${container}" psql -X --no-password -U studio_bootstrap -d studio -Atc \
  "SELECT
      has_database_privilege('studio_api','studio','CONNECT')
      AND NOT has_database_privilege('studio_api','studio','CREATE')
      AND has_schema_privilege('studio_api','public','USAGE')
      AND NOT has_schema_privilege('studio_api','public','CREATE')
      AND has_table_privilege('studio_api','users','SELECT,INSERT,UPDATE')
      AND NOT has_table_privilege('studio_api','users','DELETE')
      AND NOT has_table_privilege('studio_api','river_job','SELECT')
      AND NOT has_table_privilege('studio_worker','users','SELECT')
      AND has_column_privilege('studio_worker','user_sessions','expires_at','SELECT')
      AND NOT has_column_privilege('studio_worker','user_sessions','token_hash','SELECT')
      AND has_table_privilege('studio_worker','river_job','SELECT,INSERT,UPDATE,DELETE')")"
test "${runtime_privileges}" = "t"

docker exec "${container}" psql -X --no-password -U studio_bootstrap -d studio -v ON_ERROR_STOP=1 -Atc \
  "SELECT 'users='||count(*) FROM users; SELECT 'batches='||count(*) FROM generation_batches; SELECT 'assets='||count(*) FROM assets WHERE purged_at IS NULL;"

reference_manifest="${check_root}/asset-references.tsv"
docker exec "${container}" psql -X --no-password -U studio_bootstrap -d studio -At -F $'\t' -c \
  "SELECT storage_key,sha256 FROM assets WHERE purged_at IS NULL UNION SELECT storage_key,sha256 FROM generation_staged_outputs ORDER BY 1" > "${reference_manifest}"
reference_count=0
while IFS=$'\t' read -r storage_key expected_hash; do
  test -n "${storage_key}" || continue
  case "${storage_key}" in
    /*|*../*|../*|*\\*) echo "unsafe storage key in restored database" >&2; exit 1 ;;
  esac
  test -f "${restored_assets}/${storage_key}"
  reference_count=$((reference_count + 1))
done < "${reference_manifest}"

if test "${sample_size}" -gt 0; then
  shuf -n "${sample_size}" "${reference_manifest}" |
  while IFS=$'\t' read -r storage_key expected_hash; do
    test -n "${storage_key}" || continue
  actual_hash="$(sha256sum "${restored_assets}/${storage_key}" | awk '{print $1}')"
  test "${actual_hash}" = "${expected_hash}"
  done
fi

echo "restore check passed: owner/ACLs restored, ${reference_count} file references present, up to ${sample_size} hashes verified"
