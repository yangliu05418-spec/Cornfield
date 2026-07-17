#!/usr/bin/env bash
set -euo pipefail
umask 077

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=ops/textfile-metrics.sh
source "${script_dir}/textfile-metrics.sh"
# shellcheck source=ops/path-safety.sh
source "${script_dir}/path-safety.sh"
# shellcheck source=ops/restore-check-recovery.sh
source "${script_dir}/restore-check-recovery.sh"

check_root=""
restore_check_root=""
restore_run_id=""
container=""

cleanup() {
  recover_restore_check_if_required
}
finish() {
  local status=$?
  local cleanup_status metrics_status
  trap - EXIT INT TERM
  set +e
  cleanup
  cleanup_status=$?
  if test "${status}" = "0" && test "${cleanup_status}" != "0"; then
    status="${cleanup_status}"
  fi
  write_maintenance_textfile_metrics restore_check "${status}"
  metrics_status=$?
  if test "${metrics_status}" != "0"; then
    echo "restore-check: failed to publish node-exporter textfile metrics" >&2
    if test "${status}" = "0"; then
      status="${metrics_status}"
    fi
  fi
  exit "${status}"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

data_root="${DATA_ROOT:?DATA_ROOT must match the production asset directory}"
restore_check_root="${RESTORE_CHECK_ROOT:?RESTORE_CHECK_ROOT must be a dedicated restore filesystem}"
backup_stage="${BACKUP_STAGE:-/var/backups/internal-image-studio}"
sample_size="${RESTORE_CHECK_SAMPLE_SIZE:-20}"
postgres_image="postgres:18.4-bookworm@sha256:1961f96e6029a02c3812d7cb329a3b03a3ac2bb067058dec17b0f5596aca9296"
restore_run_id="$(openssl rand -hex 16)"
container="cornfield-restore-${restore_run_id}"
password="$(openssl rand -hex 24)"

case "${sample_size}" in
  ''|*[!0-9]*) echo "RESTORE_CHECK_SAMPLE_SIZE must be numeric" >&2; exit 2 ;;
esac

require_canonical_directory "${data_root}" DATA_ROOT
require_canonical_directory "${restore_check_root}" RESTORE_CHECK_ROOT
require_canonical_directory "${backup_stage}" BACKUP_STAGE
test "$(stat -c '%u:%a' "${backup_stage}")" = "$(id -u):700" || {
  echo "BACKUP_STAGE must be owned by the caller with mode 0700" >&2
  exit 2
}
restore_mode="$(stat -c '%a' "${restore_check_root}")"
if test "$(stat -c '%u' "${restore_check_root}")" != "$(id -u)" || (( (8#${restore_mode} & 0022) != 0 )); then
  echo "RESTORE_CHECK_ROOT must be owned by the caller and not group/world writable" >&2
  exit 2
fi
database_directory="${backup_stage}/database"
require_canonical_directory "${database_directory}" BACKUP_DATABASE_DIRECTORY
test "$(stat -c '%u:%a' "${database_directory}")" = "$(id -u):700" || {
  echo "BACKUP_DATABASE_DIRECTORY must be owned by the caller with mode 0700" >&2
  exit 2
}
command -v flock >/dev/null 2>&1 || {
  echo "flock is required for mutually exclusive backup and restore operations" >&2
  exit 2
}
for command_name in docker hostname jq openssl restic timeout; do
  command -v "${command_name}" >/dev/null 2>&1 || {
    echo "${command_name} is required for restore verification" >&2
    exit 2
  }
done
lock_file="${database_directory}/.maintenance.lock"
if test -e "${lock_file}" || test -L "${lock_file}"; then
  if ! { test -f "${lock_file}" && test ! -L "${lock_file}"; }; then
    echo "maintenance lock path is unsafe" >&2
    exit 2
  fi
fi
exec 9>"${lock_file}"
chmod 0600 "${lock_file}"
flock -n 9 || {
  echo "another Cornfield backup or restore operation is already running" >&2
  exit 1
}

# Finish cleanup from any prior SIGKILL only after this process owns the shared
# lock. ExecStopPost uses the same lock and helper.
recover_restore_check_if_required

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

check_root="${restore_check_root}/${container}"
if ! { test ! -e "${check_root}" && test ! -L "${check_root}"; }; then
  echo "restore-check invocation directory already exists" >&2
  exit 1
fi
write_restore_check_marker "${container}" "${check_root}"
install -d -m 0700 "${check_root}"
restore_target="${check_root}/snapshot"
postgres_data="${check_root}/postgres"
mkdir -p "${restore_target}" "${postgres_data}"

verified_snapshots="$(restic snapshots --json --host "$(hostname)" --tag cornfield,cornfield-verified)"
verified_snapshot_id="$(printf '%s' "${verified_snapshots}" | jq -er '
  if type != "array" or length == 0 then error("no verified Cornfield snapshot") else . end
  | max_by(.time)
  | .id
  | select(test("^[0-9a-f]{64}$"))
')" || {
  echo "restore-check: no valid verified Cornfield snapshot is available" >&2
  exit 1
}
restic restore "${verified_snapshot_id}" --target "${restore_target}"
dump_file="$(find "${restore_target}" -type f -name 'studio-*.dump' -print | sort | tail -n 1)"
test -n "${dump_file}"
restored_assets="${restore_target}${data_root}/assets"
test -d "${restored_assets}"

docker run -d --rm --name "${container}" --memory 4g --cpus 2 --pids-limit 512 \
  --label com.cornfield.maintenance=restore-check \
  --label "com.cornfield.invocation=${restore_run_id}" \
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
      AND has_column_privilege('studio_api','assets','lock_guard','UPDATE')
      AND NOT has_column_privilege('studio_api','assets','purge_pending','UPDATE')
      AND has_column_privilege('studio_api','providers','state','UPDATE')
      AND NOT has_column_privilege('studio_api','providers','enabled','UPDATE')
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
    /*|..|../*|*/..|*/../*|*\\*) echo "unsafe storage key in restored database" >&2; exit 1 ;;
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
