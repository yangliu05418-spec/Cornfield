#!/usr/bin/env bash
set -euo pipefail
umask 077

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=ops/path-safety.sh
source "${script_dir}/path-safety.sh"
# shellcheck source=ops/textfile-metrics.sh
source "${script_dir}/textfile-metrics.sh"

worker_marker_directory() {
  printf '%s' "${BACKUP_WORKER_MARKER_DIR:-/var/lib/internal-image-studio}"
}

worker_marker_path() {
  printf '%s/worker-restart-required' "$(worker_marker_directory)"
}

prepare_worker_marker_directory() {
  local directory
  local expected_uid
  directory="$(worker_marker_directory)"
  expected_uid="$(id -u)"
  case "${directory}" in
    /|*[[:space:]]*)
      echo "BACKUP_WORKER_MARKER_DIR is unsafe" >&2
      return 2
      ;;
    /*) ;;
    *)
      echo "BACKUP_WORKER_MARKER_DIR must be absolute" >&2
      return 2
      ;;
  esac
  if ! test -e "${directory}"; then
    install -d -m 0700 "${directory}"
  fi
  require_canonical_directory "${directory}" BACKUP_WORKER_MARKER_DIR
  test "$(stat -c '%u:%a' "${directory}")" = "${expected_uid}:700" || {
    echo "BACKUP_WORKER_MARKER_DIR must be owned by the caller with mode 0700" >&2
    return 2
  }
}

mark_worker_restart_required() {
  local worker_container_id="$1"
  local directory marker temporary
  [[ "${worker_container_id}" =~ ^[0-9a-f]{12,64}$ ]] || {
    echo "Worker container ID is invalid" >&2
    return 2
  }
  prepare_worker_marker_directory
  directory="$(worker_marker_directory)"
  marker="$(worker_marker_path)"
  if test -e "${marker}" || test -L "${marker}"; then
    test -f "${marker}" && test ! -L "${marker}" && test "$(stat -c '%u:%a' "${marker}")" = "$(id -u):600" || {
      echo "worker restart marker is unsafe" >&2
      return 2
    }
    return 0
  fi
  temporary="$(mktemp "${directory}/.worker-restart-required.XXXXXX")"
  printf '%s\n%s\n' "${worker_container_id}" "$(date -u +%Y-%m-%dT%H:%M:%SZ)" > "${temporary}"
  chmod 0600 "${temporary}"
  mv -f -- "${temporary}" "${marker}"
}

recover_worker_if_required() {
  local marker studio_root worker_container_id current_container_id labels
  local -a marker_fields
  marker="$(worker_marker_path)"
  if ! test -e "${marker}" && ! test -L "${marker}"; then
    return 0
  fi
  prepare_worker_marker_directory
  test -f "${marker}" && test ! -L "${marker}" && test "$(stat -c '%u:%a' "${marker}")" = "$(id -u):600" || {
    echo "worker restart marker is unsafe" >&2
    return 2
  }
  mapfile -t marker_fields < "${marker}"
  test "${#marker_fields[@]}" = "2" || {
    echo "worker restart marker is malformed" >&2
    return 2
  }
  worker_container_id="${marker_fields[0]}"
  [[ "${worker_container_id}" =~ ^[0-9a-f]{12,64}$ ]] || {
    echo "worker restart marker has an invalid container ID" >&2
    return 2
  }
  studio_root="${STUDIO_ROOT:-/opt/internal-image-studio}"
  require_canonical_directory "${studio_root}" STUDIO_ROOT
  test -f "${studio_root}/compose.yaml" || {
    echo "STUDIO_ROOT does not contain compose.yaml" >&2
    return 2
  }
  command -v timeout >/dev/null 2>&1 || {
    echo "timeout is required for bounded Worker recovery" >&2
    return 2
  }
  current_container_id="$(timeout --signal=TERM --kill-after=15s 30s \
    docker compose --file "${studio_root}/compose.yaml" --project-directory "${studio_root}" ps --all -q worker)" || return 1
  test "${current_container_id}" = "${worker_container_id}" || {
    echo "Worker container changed while a restart marker was active" >&2
    return 1
  }
  labels="$(timeout --signal=TERM --kill-after=15s 30s docker inspect --format \
    '{{ index .Config.Labels "com.docker.compose.project" }} {{ index .Config.Labels "com.docker.compose.service" }}' \
    "${worker_container_id}")" || return 1
  test "${labels}" = "internal-image-studio worker" || {
    echo "Worker container labels do not match the reviewed Compose project" >&2
    return 1
  }
  if ! timeout --signal=TERM --kill-after=15s "${BACKUP_WORKER_COMMAND_TIMEOUT_SECONDS:-150}s" \
      docker compose --file "${studio_root}/compose.yaml" --project-directory "${studio_root}" \
      start --wait --wait-timeout "${BACKUP_WORKER_WAIT_SECONDS:-120}" worker >/dev/null; then
    echo "failed to restore the Worker; restart marker retained at ${marker}" >&2
    return 1
  fi
  if ! rm -f -- "${marker}"; then
    echo "Worker is running but the restart marker could not be removed: ${marker}" >&2
    return 1
  fi
}

if test "${BASH_SOURCE[0]}" = "$0"; then
  recovery_status=0
  metrics_status=0
  marker_was_present=false
  if test -e "$(worker_marker_path)" || test -L "$(worker_marker_path)"; then
    marker_was_present=true
  fi
  backup_stage="${BACKUP_STAGE:-/var/backups/internal-image-studio}"
  require_canonical_directory "${backup_stage}" BACKUP_STAGE || recovery_status=$?
  database_directory="${backup_stage}/database"
  if test "${recovery_status}" = "0"; then
    require_canonical_directory "${database_directory}" BACKUP_DATABASE_DIRECTORY || recovery_status=$?
  fi
  if test "${recovery_status}" = "0" && test "$(stat -c '%u:%a' "${database_directory}")" != "$(id -u):700"; then
    echo "BACKUP_DATABASE_DIRECTORY must be owned by the caller with mode 0700" >&2
    recovery_status=2
  fi
  if test "${recovery_status}" = "0"; then
    command -v flock >/dev/null 2>&1 || {
      echo "flock is required for Worker recovery serialization" >&2
      recovery_status=2
    }
  fi
  if test "${recovery_status}" = "0"; then
    lock_file="${database_directory}/.maintenance.lock"
    if test -e "${lock_file}" || test -L "${lock_file}"; then
      test -f "${lock_file}" && test ! -L "${lock_file}" || {
        echo "maintenance lock path is unsafe" >&2
        recovery_status=2
      }
    fi
  fi
  if test "${recovery_status}" = "0"; then
    exec 9>"${lock_file}"
    chmod 0600 "${lock_file}"
    flock -w "${BACKUP_RECOVERY_LOCK_WAIT_SECONDS:-15}" 9 || {
      echo "timed out waiting to serialize Worker recovery" >&2
      recovery_status=1
    }
  fi
  if test "${recovery_status}" = "0"; then
    recover_worker_if_required || recovery_status=$?
  fi
  # systemd supplies SERVICE_RESULT to ExecStopPost. A SIGKILL bypasses the
  # backup script's EXIT trap, so publish the failed run here as well. Normal
  # successful service shutdowns leave the existing success metric untouched.
  if test "${marker_was_present}" = "true" || \
      { test -n "${SERVICE_RESULT:-}" && test "${SERVICE_RESULT}" != "success"; } || \
      test "${recovery_status}" != "0"; then
    write_maintenance_textfile_metrics backup 1 || metrics_status=$?
  fi
  if test "${recovery_status}" != "0"; then
    exit "${recovery_status}"
  fi
  exit "${metrics_status}"
fi
