#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=ops/textfile-metrics.sh
source "${script_dir}/textfile-metrics.sh"

metrics_dir="$(mktemp -d /tmp/cornfield-textfile-test.XXXXXX)"
cleanup() {
  case "${metrics_dir}" in
    /tmp/cornfield-textfile-test.*) rm -rf -- "${metrics_dir}" ;;
  esac
}
trap cleanup EXIT
export NODE_EXPORTER_TEXTFILE_DIR="${metrics_dir}"

write_maintenance_textfile_metrics backup 0
metric_file="${metrics_dir}/cornfield_backup.prom"
test "$(stat -c '%a' "${metric_file}")" = "644"
first_success="$(awk '$1 == "image_studio_backup_last_success_timestamp_seconds" { print $2 }' "${metric_file}")"
test "${first_success}" -gt 0

write_maintenance_textfile_metrics backup 1
grep -qx 'image_studio_backup_last_run_success 0' "${metric_file}"
second_success="$(awk '$1 == "image_studio_backup_last_success_timestamp_seconds" { print $2 }' "${metric_file}")"
test "${second_success}" = "${first_success}"
test -z "$(find "${metrics_dir}" -maxdepth 1 -type f -name '.cornfield_*.??????' -print -quit)"

rm -f -- "${metric_file}"
ln -s /dev/null "${metric_file}"
if write_maintenance_textfile_metrics backup 0; then
  echo "textfile metrics accepted a symlink target" >&2
  exit 1
fi

echo "textfile metrics tests passed"
