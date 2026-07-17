#!/usr/bin/env bash

# Publish the outcome of a systemd maintenance job for node-exporter's
# textfile collector. The previous success timestamp survives failed runs.
write_maintenance_textfile_metrics() {
  local operation="$1"
  local exit_status="$2"
  local metrics_dir="${NODE_EXPORTER_TEXTFILE_DIR:-/var/lib/node_exporter/textfile_collector}"
  local file_prefix metric_prefix final_file temporary_file now last_success success

  case "${operation}" in
    backup|restore_check) ;;
    *) return 2 ;;
  esac
  case "${exit_status}" in
    ''|*[!0-9]*) return 2 ;;
  esac
  case "${metrics_dir}" in
    /) return 2 ;;
    /*) ;;
    *) return 2 ;;
  esac
  test -d "${metrics_dir}" && test ! -L "${metrics_dir}" && test -w "${metrics_dir}" || return 1

  file_prefix="cornfield_${operation}"
  metric_prefix="image_studio_${operation}"
  final_file="${metrics_dir}/${file_prefix}.prom"
  if test -e "${final_file}" && { test ! -f "${final_file}" || test -L "${final_file}"; }; then
    return 1
  fi

  now="$(date +%s)" || return 1
  last_success=0
  if test -f "${final_file}"; then
    last_success="$(awk -v metric="${metric_prefix}_last_success_timestamp_seconds" \
      '$1 == metric && $2 ~ /^[0-9]+$/ { value=$2 } END { print value }' "${final_file}")"
    last_success="${last_success:-0}"
  fi
  success=0
  if test "${exit_status}" = "0"; then
    success=1
    last_success="${now}"
  fi

  temporary_file="$(mktemp "${metrics_dir}/.${file_prefix}.XXXXXX")" || return 1
  if ! {
    printf '# HELP %s_last_run_success Whether the most recent %s run completed successfully.\n' "${metric_prefix}" "${operation}"
    printf '# TYPE %s_last_run_success gauge\n' "${metric_prefix}"
    printf '%s_last_run_success %s\n' "${metric_prefix}" "${success}"
    printf '# HELP %s_last_run_timestamp_seconds Unix timestamp of the most recent %s run.\n' "${metric_prefix}" "${operation}"
    printf '# TYPE %s_last_run_timestamp_seconds gauge\n' "${metric_prefix}"
    printf '%s_last_run_timestamp_seconds %s\n' "${metric_prefix}" "${now}"
    printf '# HELP %s_last_success_timestamp_seconds Unix timestamp of the most recent successful %s run.\n' "${metric_prefix}" "${operation}"
    printf '# TYPE %s_last_success_timestamp_seconds gauge\n' "${metric_prefix}"
    printf '%s_last_success_timestamp_seconds %s\n' "${metric_prefix}" "${last_success}"
  } > "${temporary_file}" || ! chmod 0644 "${temporary_file}" || ! mv -f -- "${temporary_file}" "${final_file}"; then
    rm -f -- "${temporary_file}"
    return 1
  fi
}
