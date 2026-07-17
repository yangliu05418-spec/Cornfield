#!/usr/bin/env bash
set -euo pipefail
umask 077

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=ops/textfile-metrics.sh
source "${script_dir}/textfile-metrics.sh"
# shellcheck source=ops/path-safety.sh
source "${script_dir}/path-safety.sh"
# shellcheck source=ops/backup-worker-recovery.sh
source "${script_dir}/backup-worker-recovery.sh"

dump_part=""
dump_final=""
database_directory=""
worker_container=""
worker_recovery_owned=false
pending_snapshot_tag=""
backup_host=""

cleanup_staging() {
  local path status=0
  if test -z "${database_directory}"; then
    return 0
  fi
  for path in "${dump_part}" "${dump_final}"; do
    if test -n "${path}" && [[ "${path}" == "${database_directory}"/studio-*.dump || "${path}" == "${database_directory}"/studio-*.dump.part ]]; then
      rm -f -- "${path}" || status=$?
    fi
  done
  return "${status}"
}

discard_pending_snapshot() {
  local snapshots_json snapshot_id
  if test -z "${pending_snapshot_tag}"; then
    return 0
  fi
  snapshots_json="$(restic snapshots --json --host "${backup_host}" --tag "${pending_snapshot_tag}")" || return 1
  snapshot_id="$(printf '%s' "${snapshots_json}" | jq -er '
    if type != "array" or length > 1 then error("unexpected pending snapshot count")
    elif length == 0 then ""
    else .[0].id | select(test("^[0-9a-f]{64}$"))
    end
  ')" || return 1
  if test -n "${snapshot_id}" && ! restic forget "${snapshot_id}"; then
    echo "backup: failed to discard pending Restic snapshot tagged ${pending_snapshot_tag}" >&2
    return 1
  fi
  pending_snapshot_tag=""
}

finish() {
  local status=$?
  local recovery_status=0 pending_status=0 cleanup_status=0 metrics_status=0
  trap - EXIT INT TERM
  set +e
  if test "${worker_recovery_owned}" = "true"; then
    recover_worker_if_required || recovery_status=$?
    if test "${recovery_status}" = "0"; then
      worker_recovery_owned=false
    fi
  fi
  discard_pending_snapshot || pending_status=$?
  cleanup_staging || cleanup_status=$?
  if test "${status}" = "0" && test "${recovery_status}" != "0"; then
    status="${recovery_status}"
  fi
  if test "${status}" = "0" && test "${cleanup_status}" != "0"; then
    status="${cleanup_status}"
  fi
  if test "${status}" = "0" && test "${pending_status}" != "0"; then
    status="${pending_status}"
  fi
  write_maintenance_textfile_metrics backup "${status}" || metrics_status=$?
  if test "${metrics_status}" != "0"; then
    echo "backup: failed to publish node-exporter textfile metrics" >&2
    if test "${status}" = "0"; then
      status="${metrics_status}"
    fi
  fi
  exit "${status}"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

studio_root="${STUDIO_ROOT:-/opt/internal-image-studio}"
data_root="${DATA_ROOT:?DATA_ROOT must be the absolute asset directory used by Compose and Nginx}"
backup_stage="${BACKUP_STAGE:-/var/backups/internal-image-studio}"

# Validate every read/write root before any Docker operation or deletion. The
# equality check rejects symlinks in parent components and non-canonical paths.
require_canonical_directory "${studio_root}" STUDIO_ROOT
require_canonical_directory "${data_root}" DATA_ROOT
require_canonical_directory "${backup_stage}" BACKUP_STAGE
test "$(stat -c '%u:%a' "${backup_stage}")" = "$(id -u):700" || {
  echo "BACKUP_STAGE must be owned by the caller with mode 0700" >&2
  exit 2
}
test -d "${data_root}/assets"
test -f "${studio_root}/compose.yaml"

database_directory="${backup_stage}/database"
if ! test -e "${database_directory}"; then
  install -d -m 0700 "${database_directory}"
fi
require_canonical_directory "${database_directory}" BACKUP_DATABASE_DIRECTORY
test "$(stat -c '%u:%a' "${database_directory}")" = "$(id -u):700" || {
  echo "BACKUP_DATABASE_DIRECTORY must be owned by the caller with mode 0700" >&2
  exit 2
}
command -v flock >/dev/null 2>&1 || {
  echo "flock is required for mutually exclusive backup and restore operations" >&2
  exit 2
}
for command_name in hostname jq openssl restic; do
  command -v "${command_name}" >/dev/null 2>&1 || {
    echo "${command_name} is required for verified backups" >&2
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

# A SIGKILL can bypass EXIT traps. Once the exclusive lock is held, remove
# staging files left by a prior interrupted run. No local plaintext dump is a
# backup source; Restic is the durable copy.
unsafe_staging="$(find "${database_directory}" -maxdepth 1 \( -name 'studio-*.dump' -o -name 'studio-*.dump.part' \) ! -type f -print -quit)"
test -z "${unsafe_staging}" || {
  echo "unsafe backup staging entry: ${unsafe_staging}" >&2
  exit 2
}
find "${database_directory}" -maxdepth 1 -type f \( -name 'studio-*.dump' -o -name 'studio-*.dump.part' \) -delete

cd "${studio_root}"
studio_compose() {
  docker compose --file "${studio_root}/compose.yaml" --project-directory "${studio_root}" "$@"
}
# A marker left by an earlier failed recovery belongs to the previous run. It
# is safe to act on only after this process owns the shared maintenance lock.
recover_worker_if_required
worker_container="$(studio_compose ps --all -q worker)"
if ! { test -n "${worker_container}" && test "$(printf '%s\n' "${worker_container}" | wc -l)" = "1"; }; then
  echo "backup requires exactly one existing Worker container" >&2
  exit 1
fi

worker_state() {
  docker inspect --format '{{.State.Status}}' "${worker_container}"
}

assert_worker_inactive() {
  local state
  state="$(worker_state)" || return 1
  case "${state}" in
    created|exited|dead) return 0 ;;
    *)
      echo "Worker did not remain quiesced (state=${state})" >&2
      return 1
      ;;
  esac
}

initial_worker_state="$(worker_state)"
case "${initial_worker_state}" in
  running|restarting|paused)
    # The marker is written before stop. Normal EXIT recovery handles ordinary
    # failures; systemd ExecStopPost handles SIGKILL; restart: always covers a
    # host or Docker daemon restart while the container is manually stopped.
    mark_worker_restart_required "${worker_container}"
    worker_recovery_owned=true
    ;;
  created|exited|dead) ;;
  *)
    echo "unsupported Worker state: ${initial_worker_state}" >&2
    exit 1
    ;;
esac
studio_compose stop -t 120 worker >/dev/null
assert_worker_inactive

dump_part="${database_directory}/studio-current.dump.part"
dump_final="${database_directory}/studio-current.dump"
studio_compose exec -T postgres pg_dump --no-password -U studio_bootstrap -d studio --format=custom > "${dump_part}"
mv -- "${dump_part}" "${dump_final}"
dump_part=""
assert_worker_inactive

backup_targets=("${dump_final}" "${data_root}" "${studio_root}/config" "${studio_root}/compose.yaml" "${studio_root}/.env")
if test -d "${studio_root}/secrets"; then
  backup_targets+=("${studio_root}/secrets")
fi
backup_host="$(hostname)"
backup_run_id="$(openssl rand -hex 16)"
pending_snapshot_tag="cornfield-run-${backup_run_id}"
restic_backup_status=0
restic backup --tag cornfield --tag cornfield-pending --tag "${pending_snapshot_tag}" "${backup_targets[@]}" || restic_backup_status=$?
snapshot_json="$(restic snapshots --json --host "${backup_host}" --tag "${pending_snapshot_tag}")" || {
  echo "backup: cannot verify the Restic snapshot created by this run" >&2
  exit 1
}
expected_paths_json="$(printf '%s\n' "${backup_targets[@]}" | jq -Rsc 'split("\n")[:-1] | sort')"
snapshot_id="$(printf '%s' "${snapshot_json}" | jq -er --argjson expected "${expected_paths_json}" '
  if type != "array" or length != 1 then error("expected one snapshot") else .[0] end
  | select((.paths | sort) == $expected)
  | .id
  | select(test("^[0-9a-f]{64}$"))
')" || {
  echo "backup: invalid Restic snapshot identity or path set" >&2
  exit 1
}
if test "${restic_backup_status}" != "0"; then
  echo "backup: Restic backup failed with status ${restic_backup_status}; any partial snapshot will be discarded" >&2
  exit "${restic_backup_status}"
fi
assert_worker_inactive

# Only a snapshot produced without source read errors and while the Worker
# remained quiesced becomes eligible for restore or retention.
restic tag --add cornfield-verified --remove cornfield-pending "${snapshot_id}"
promoted_json="$(restic snapshots --json --host "${backup_host}" --tag "${pending_snapshot_tag}")"
printf '%s' "${promoted_json}" | jq -e --arg run "${pending_snapshot_tag}" --argjson expected "${expected_paths_json}" '
  type == "array" and length == 1 and
  (.[0].paths | sort) == $expected and
  (.[0].tags | index("cornfield")) != null and
  (.[0].tags | index("cornfield-verified")) != null and
  (.[0].tags | index($run)) != null and
  (.[0].tags | index("cornfield-pending")) == null
' >/dev/null || {
  echo "backup: Restic snapshot promotion could not be verified" >&2
  exit 1
}
pending_snapshot_tag=""

# The coherent database/filesystem snapshot is sealed. Restore the Worker
# before remote retention so prune latency does not extend generation downtime.
recover_worker_if_required
worker_recovery_owned=false
restic forget --host "${backup_host}" --tag cornfield,cornfield-verified --group-by host \
  --keep-daily 7 --keep-weekly 4 --prune
