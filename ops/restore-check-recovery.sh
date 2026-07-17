#!/usr/bin/env bash
set -euo pipefail
umask 077

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=ops/path-safety.sh
source "${script_dir}/path-safety.sh"
# shellcheck source=ops/textfile-metrics.sh
source "${script_dir}/textfile-metrics.sh"

restore_check_marker_path() {
  printf '%s/database/restore-check-active' "${BACKUP_STAGE:-/var/backups/internal-image-studio}"
}

validate_restore_check_target() {
  local container="$1"
  local check_root="$2"
  local restore_root="${RESTORE_CHECK_ROOT:?RESTORE_CHECK_ROOT is required}"

  [[ "${container}" =~ ^cornfield-restore-[0-9a-f]{32}$ ]] || {
    echo "restore-check recovery marker has an invalid container name" >&2
    return 2
  }
  require_canonical_directory "${restore_root}" RESTORE_CHECK_ROOT
  test "${check_root}" = "${restore_root}/${container}" || {
    echo "restore-check recovery marker points outside its exact invocation directory" >&2
    return 2
  }
  if test -e "${check_root}" || test -L "${check_root}"; then
    require_canonical_directory "${check_root}" RESTORE_CHECK_TARGET
    test "$(stat -c '%u' "${check_root}")" = "$(id -u)" || {
      echo "restore-check target is not owned by the caller" >&2
      return 2
    }
  fi
}

write_restore_check_marker() {
  local container="$1"
  local check_root="$2"
  local marker temporary database_directory

  validate_restore_check_target "${container}" "${check_root}"
  database_directory="${BACKUP_STAGE:-/var/backups/internal-image-studio}/database"
  require_canonical_directory "${database_directory}" BACKUP_DATABASE_DIRECTORY
  marker="$(restore_check_marker_path)"
  if test -e "${marker}" || test -L "${marker}"; then
    echo "restore-check recovery marker already exists" >&2
    return 1
  fi
  temporary="$(mktemp "${database_directory}/.restore-check-active.XXXXXX")"
  if ! printf '%s\n%s\n' "${container}" "${check_root}" > "${temporary}" || \
      ! chmod 0600 "${temporary}" || ! mv -- "${temporary}" "${marker}"; then
    rm -f -- "${temporary}"
    return 1
  fi
}

recover_restore_check_if_required() {
  local marker container check_root container_ids run_id labels
  local -a fields

  marker="$(restore_check_marker_path)"
  if ! test -e "${marker}" && ! test -L "${marker}"; then
    return 0
  fi
  if ! { test -f "${marker}" && test ! -L "${marker}" && test "$(stat -c '%u:%a' "${marker}")" = "$(id -u):600"; }; then
    echo "restore-check recovery marker is unsafe" >&2
    return 2
  fi
  mapfile -t fields < "${marker}"
  test "${#fields[@]}" = "2" || {
    echo "restore-check recovery marker is malformed" >&2
    return 2
  }
  container="${fields[0]}"
  check_root="${fields[1]}"
  validate_restore_check_target "${container}" "${check_root}"
  run_id="${container#cornfield-restore-}"

  command -v timeout >/dev/null 2>&1 || return 2
  container_ids="$(timeout --signal=TERM --kill-after=15s 60s docker ps -aq \
    --filter "name=^/${container}$")" || return 1
  if test -n "${container_ids}"; then
    test "$(printf '%s\n' "${container_ids}" | wc -l)" = "1" || {
      echo "restore-check recovery matched multiple containers" >&2
      return 1
    }
    labels="$(timeout --signal=TERM --kill-after=15s 30s docker inspect --format \
      '{{ index .Config.Labels "com.cornfield.maintenance" }} {{ index .Config.Labels "com.cornfield.invocation" }}' \
      "${container_ids}")" || return 1
    test "${labels}" = "restore-check ${run_id}" || {
      echo "restore-check container labels do not match its recovery marker" >&2
      return 1
    }
    timeout --signal=TERM --kill-after=15s 60s docker rm -f "${container_ids}" >/dev/null || return 1
  fi
  if test -e "${check_root}" || test -L "${check_root}"; then
    rm -rf -- "${check_root}" || return 1
  fi
  rm -f -- "${marker}" || return 1
}

if test "${BASH_SOURCE[0]}" = "$0"; then
  recovery_status=0
  metrics_status=0
  marker_was_present=false
  if test -e "$(restore_check_marker_path)" || test -L "$(restore_check_marker_path)"; then
    marker_was_present=true
  fi
  backup_stage="${BACKUP_STAGE:-/var/backups/internal-image-studio}"
  database_directory="${backup_stage}/database"
  require_canonical_directory "${backup_stage}" BACKUP_STAGE || recovery_status=$?
  if test "${recovery_status}" = "0"; then
    require_canonical_directory "${database_directory}" BACKUP_DATABASE_DIRECTORY || recovery_status=$?
  fi
  if test "${recovery_status}" = "0" && test "$(stat -c '%u:%a' "${database_directory}")" != "$(id -u):700"; then
    echo "BACKUP_DATABASE_DIRECTORY must be owned by the caller with mode 0700" >&2
    recovery_status=2
  fi
  if test "${recovery_status}" = "0"; then
    command -v flock >/dev/null 2>&1 || {
      echo "flock is required for restore-check recovery serialization" >&2
      recovery_status=2
    }
  fi
  if test "${recovery_status}" = "0"; then
    lock_file="${database_directory}/.maintenance.lock"
    if test -e "${lock_file}" || test -L "${lock_file}"; then
      if ! { test -f "${lock_file}" && test ! -L "${lock_file}"; }; then
        echo "maintenance lock path is unsafe" >&2
        recovery_status=2
      fi
    fi
  fi
  if test "${recovery_status}" = "0"; then
    exec 9>"${lock_file}"
    chmod 0600 "${lock_file}"
    flock -w "${RESTORE_RECOVERY_LOCK_WAIT_SECONDS:-15}" 9 || {
      echo "timed out waiting to serialize restore-check recovery" >&2
      recovery_status=1
    }
  fi
  if test "${recovery_status}" = "0"; then
    recover_restore_check_if_required || recovery_status=$?
  fi
  if test "${marker_was_present}" = "true" || \
      { test -n "${SERVICE_RESULT:-}" && test "${SERVICE_RESULT}" != "success"; } || \
      test "${recovery_status}" != "0"; then
    write_maintenance_textfile_metrics restore_check 1 || metrics_status=$?
  fi
  if test "${recovery_status}" != "0"; then
    exit "${recovery_status}"
  fi
  exit "${metrics_status}"
fi
