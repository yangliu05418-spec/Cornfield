#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=ops/path-safety.sh
source "${script_dir}/path-safety.sh"
# shellcheck source=ops/backup-worker-recovery.sh
source "${script_dir}/backup-worker-recovery.sh"
# shellcheck source=ops/restore-check-recovery.sh
source "${script_dir}/restore-check-recovery.sh"

test_root="$(mktemp -d /tmp/cornfield-maintenance-test.XXXXXX)"
cleanup() {
  case "${test_root}" in
    /tmp/cornfield-maintenance-test.*) rm -rf -- "${test_root}" ;;
  esac
}
trap cleanup EXIT

canonical="${test_root}/canonical"
mkdir -p "${canonical}/data"
require_canonical_directory "${canonical}/data" TEST_PATH
ln -s "${canonical}/data" "${test_root}/leaf-link"
if test -L "${test_root}/leaf-link"; then
  if require_canonical_directory "${test_root}/leaf-link" TEST_PATH 2>/dev/null; then
    echo "path safety accepted a leaf symlink" >&2
    exit 1
  fi
fi
ln -s "${canonical}" "${test_root}/parent-link"
if test -L "${test_root}/parent-link"; then
  if require_canonical_directory "${test_root}/parent-link/data" TEST_PATH 2>/dev/null; then
    echo "path safety accepted a parent symlink" >&2
    exit 1
  fi
fi
if require_canonical_directory "${canonical}/data/../data" TEST_PATH 2>/dev/null; then
  echo "path safety accepted a non-canonical path" >&2
  exit 1
fi
if require_canonical_directory "${test_root}/missing" TEST_PATH 2>/dev/null; then
  echo "path safety accepted a missing directory" >&2
  exit 1
fi

# The production scripts target Linux hosts and assert POSIX uid/mode values.
# Git Bash on Windows emulates symlinks and chmod differently, so CI performs
# the complete fault-injection matrix on Ubuntu.
if test "$(uname -s)" != "Linux"; then
  echo "maintenance safety Linux fault injection skipped on $(uname -s)"
  exit 0
fi

stub_directory="${test_root}/bin"
mkdir -p "${stub_directory}"
docker_stub="${stub_directory}/docker"
restic_stub="${stub_directory}/restic"
cat > "${docker_stub}" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'docker %s\n' "$*" >> "${MOCK_LOG}"
if test "$1" = "inspect"; then
  case "$*" in
    *State.Status*) cat "${MOCK_STATE}" ;;
    *com.docker.compose.project*) printf '%s\n' 'internal-image-studio worker' ;;
    *com.cornfield.maintenance*) printf 'restore-check %s\n' "${MOCK_RESTORE_RUN_ID:?}" ;;
    *) exit 2 ;;
  esac
  exit 0
fi
if test "$1" = "ps"; then
  if test "${MOCK_RESTORE_CONTAINER_PRESENT:-true}" = "true"; then
    printf '%s\n' restore-container-id
  fi
  exit 0
fi
if test "$1" = "rm"; then
  exit 0
fi
test "$1" = "compose"
shift
while test "$#" -gt 0; do
  case "$1" in
    --file|--project-directory) shift 2 ;;
    *) break ;;
  esac
done
case "$1" in
  ps)
    printf '%064d\n' 0 | tr 0 b
    ;;
  stop)
    if test "${MOCK_STOP_STAYS_ACTIVE:-false}" != "true"; then
      printf '%s\n' exited > "${MOCK_STATE}"
    fi
    ;;
  exec)
    printf '%s' partial-dump
    test "${MOCK_PGDUMP_FAIL:-false}" != "true"
    ;;
  start)
    if test "${MOCK_UP_FAIL:-false}" = "true"; then
      exit 1
    fi
    printf '%s\n' running > "${MOCK_STATE}"
    ;;
  *)
    echo "unexpected docker compose command: $*" >&2
    exit 2
    ;;
esac
STUB
cat > "${restic_stub}" <<'STUB'
#!/usr/bin/env bash
set -euo pipefail
printf 'restic %s\n' "$*" >> "${MOCK_LOG}"
command="$1"
shift
case "${command}" in
  backup)
    run_tag=""
    paths=()
    while test "$#" -gt 0; do
      case "$1" in
        --tag)
          shift
          case "${1:?missing tag value}" in
            cornfield-run-*) run_tag="$1" ;;
          esac
          ;;
        *) paths+=("$1") ;;
      esac
      shift
    done
    test -n "${run_tag}"
    {
      printf '%s\n' "${run_tag}"
      printf '%s\n' pending
      printf '%s\n' "${paths[@]}"
    } > "${MOCK_RESTIC_STATE}"
    if test "${MOCK_RESTIC_FAIL:-false}" = "true"; then
      exit 3
    fi
    ;;
  snapshots)
    requested_tag=""
    while test "$#" -gt 0; do
      if test "$1" = "--tag"; then
        shift
        requested_tag="${1:?missing tag value}"
      fi
      shift
    done
    current_tag="$(head -n 1 "${MOCK_RESTIC_STATE}")"
    snapshot_state="$(sed -n '2p' "${MOCK_RESTIC_STATE}")"
    if test -n "${current_tag}" && test "${current_tag}" = "${requested_tag}"; then
      paths_json="$(tail -n +3 "${MOCK_RESTIC_STATE}" | jq -Rsc 'split("\n")[:-1]')"
      if test "${snapshot_state}" = verified; then
        tags="[\"cornfield\",\"cornfield-verified\",\"${current_tag}\"]"
      else
        tags="[\"cornfield\",\"cornfield-pending\",\"${current_tag}\"]"
      fi
      printf '[{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","time":"2026-01-01T00:00:00Z","tags":%s,"paths":%s}]\n' "${tags}" "${paths_json}"
    else
      printf '[]\n'
    fi
    ;;
  tag)
    test "${MOCK_TAG_FAIL:-false}" != "true"
    current_tag="$(head -n 1 "${MOCK_RESTIC_STATE}")"
    case "${current_tag}" in
      cornfield-run-*)
        { printf '%s\n' "${current_tag}"; printf '%s\n' verified; tail -n +3 "${MOCK_RESTIC_STATE}"; } > "${MOCK_RESTIC_STATE}.new"
        mv "${MOCK_RESTIC_STATE}.new" "${MOCK_RESTIC_STATE}"
        ;;
      *) exit 1 ;;
    esac
    ;;
  forget)
    requested_tag=""
    explicit_snapshot_id=""
    retention=false
    while test "$#" -gt 0; do
      case "$1" in
        --tag) shift; requested_tag="${1:?missing tag value}" ;;
        --keep-*) retention=true ;;
        [0-9a-f][0-9a-f]*) explicit_snapshot_id="$1" ;;
      esac
      shift
    done
    if test "${retention}" = "true"; then
      test "${MOCK_FORGET_FAIL:-false}" != "true"
    else
      test "${MOCK_PENDING_FORGET_FAIL:-false}" != "true"
      current_tag="$(head -n 1 "${MOCK_RESTIC_STATE}")"
      if test -n "${explicit_snapshot_id}" || test "${current_tag}" = "${requested_tag}"; then
        : > "${MOCK_RESTIC_STATE}"
      fi
    fi
    ;;
  *) exit 2 ;;
esac
STUB
chmod 0755 "${docker_stub}" "${restic_stub}"
worker_id="$(printf '%064d' 0 | tr 0 b)"

setup_case() {
  local name="$1"
  case_root="${test_root}/${name}"
  studio_root="${case_root}/studio"
  data_root="${case_root}/data"
  backup_stage="${case_root}/backup"
  metrics_root="${case_root}/metrics"
  marker_root="${case_root}/marker"
  mock_state="${case_root}/worker.state"
  mock_restic_state="${case_root}/restic.state"
  mock_log="${case_root}/commands.log"
  mkdir -p "${studio_root}/config" "${data_root}/assets" "${backup_stage}" "${metrics_root}"
  chmod 0700 "${backup_stage}"
  printf '%s\n' services: > "${studio_root}/compose.yaml"
  printf '%s\n' APP_ENV=production > "${studio_root}/.env"
  : > "${mock_log}"
  : > "${mock_restic_state}"
}

run_backup() {
  env \
    PATH="${stub_directory}:${PATH}" \
    STUDIO_ROOT="${studio_root}" \
    DATA_ROOT="${data_root}" \
    BACKUP_STAGE="${backup_stage}" \
    NODE_EXPORTER_TEXTFILE_DIR="${metrics_root}" \
    BACKUP_WORKER_MARKER_DIR="${marker_root}" \
    MOCK_STATE="${mock_state}" \
    MOCK_RESTIC_STATE="${mock_restic_state}" \
    MOCK_LOG="${mock_log}" \
    MOCK_PGDUMP_FAIL="${MOCK_PGDUMP_FAIL:-false}" \
    MOCK_RESTIC_FAIL="${MOCK_RESTIC_FAIL:-false}" \
    MOCK_FORGET_FAIL="${MOCK_FORGET_FAIL:-false}" \
    MOCK_STOP_STAYS_ACTIVE="${MOCK_STOP_STAYS_ACTIVE:-false}" \
    MOCK_UP_FAIL="${MOCK_UP_FAIL:-false}" \
    MOCK_PENDING_FORGET_FAIL="${MOCK_PENDING_FORGET_FAIL:-false}" \
    MOCK_TAG_FAIL="${MOCK_TAG_FAIL:-false}" \
    "${script_dir}/backup.sh"
}

assert_no_dump_staging() {
  test -z "$(find "${backup_stage}/database" -maxdepth 1 -type f \( -name 'studio-*.dump' -o -name 'studio-*.dump.part' \) -print -quit)"
}

assert_no_staging() {
  assert_no_dump_staging
  test ! -e "${marker_root}/worker-restart-required"
}

setup_case success
printf '%s\n' running > "${mock_state}"
run_backup
test "$(cat "${mock_state}")" = running
assert_no_staging
grep -q '^restic backup ' "${mock_log}"
grep -q '^restic forget ' "${mock_log}"
test "$(sed -n '2p' "${mock_restic_state}")" = verified
grep -qx 'image_studio_backup_last_run_success 1' "${metrics_root}/cornfield_backup.prom"

for failure in pgdump restic forget; do
  setup_case "${failure}-failure"
  printf '%s\n' running > "${mock_state}"
  MOCK_PGDUMP_FAIL=false
  MOCK_RESTIC_FAIL=false
  MOCK_FORGET_FAIL=false
  case "${failure}" in
    pgdump) MOCK_PGDUMP_FAIL=true ;;
    restic) MOCK_RESTIC_FAIL=true ;;
    forget) MOCK_FORGET_FAIL=true ;;
  esac
  if run_backup; then
    echo "backup unexpectedly succeeded during ${failure} failure" >&2
    exit 1
  fi
  test "$(cat "${mock_state}")" = running
  assert_no_staging
  if test "${failure}" = restic; then
    test ! -s "${mock_restic_state}"
  fi
  grep -qx 'image_studio_backup_last_run_success 0' "${metrics_root}/cornfield_backup.prom"
done
unset MOCK_PGDUMP_FAIL MOCK_RESTIC_FAIL MOCK_FORGET_FAIL

setup_case snapshot-tag-failure
printf '%s\n' running > "${mock_state}"
MOCK_TAG_FAIL=true
if run_backup; then
  echo "backup unexpectedly succeeded when snapshot promotion failed" >&2
  exit 1
fi
unset MOCK_TAG_FAIL
test ! -s "${mock_restic_state}"
test "$(cat "${mock_state}")" = running
assert_no_staging

setup_case worker-recovery-failure
printf '%s\n' running > "${mock_state}"
MOCK_UP_FAIL=true
if run_backup; then
  echo "backup unexpectedly succeeded when Worker recovery failed" >&2
  exit 1
fi
unset MOCK_UP_FAIL
test "$(cat "${mock_state}")" = exited
assert_no_dump_staging
test -f "${marker_root}/worker-restart-required"
test "$(sed -n '2p' "${mock_restic_state}")" = verified
grep -qx 'image_studio_backup_last_run_success 0' "${metrics_root}/cornfield_backup.prom"

setup_case restarting
printf '%s\n' restarting > "${mock_state}"
run_backup
test "$(cat "${mock_state}")" = running
assert_no_staging

setup_case initially-stopped
printf '%s\n' exited > "${mock_state}"
run_backup
test "$(cat "${mock_state}")" = exited
assert_no_staging
if grep -q '^docker compose .* start ' "${mock_log}"; then
  echo "backup restarted a Worker that was initially stopped" >&2
  exit 1
fi

setup_case stop-not-quiesced
printf '%s\n' running > "${mock_state}"
MOCK_STOP_STAYS_ACTIVE=true
if run_backup; then
  echo "backup continued while Worker remained active" >&2
  exit 1
fi
unset MOCK_STOP_STAYS_ACTIVE
test "$(cat "${mock_state}")" = running
assert_no_staging
if grep -q '^docker compose .* exec ' "${mock_log}"; then
  echo "backup ran pg_dump before Worker quiesced" >&2
  exit 1
fi

setup_case stale-staging
mkdir -p "${backup_stage}/database"
chmod 0700 "${backup_stage}/database"
printf old > "${backup_stage}/database/studio-old.dump"
printf old > "${backup_stage}/database/studio-old.dump.part"
printf '%s\n' running > "${mock_state}"
run_backup
assert_no_staging

setup_case marker-recovery
printf '%s\n' exited > "${mock_state}"
export PATH="${stub_directory}:${PATH}"
export STUDIO_ROOT="${studio_root}"
export BACKUP_WORKER_MARKER_DIR="${marker_root}"
export MOCK_STATE="${mock_state}"
export MOCK_LOG="${mock_log}"
mark_worker_restart_required "${worker_id}"
test "$(stat -c '%a' "${marker_root}/worker-restart-required")" = 600
recover_worker_if_required
test "$(cat "${mock_state}")" = running
test ! -e "${marker_root}/worker-restart-required"

setup_case exec-stop-post
printf '%s\n' exited > "${mock_state}"
mkdir -p "${backup_stage}/database"
chmod 0700 "${backup_stage}/database"
export STUDIO_ROOT="${studio_root}"
export BACKUP_WORKER_MARKER_DIR="${marker_root}"
export MOCK_STATE="${mock_state}"
export MOCK_LOG="${mock_log}"
mark_worker_restart_required "${worker_id}"
env \
  PATH="${stub_directory}:${PATH}" \
  STUDIO_ROOT="${studio_root}" \
  BACKUP_STAGE="${backup_stage}" \
  BACKUP_WORKER_MARKER_DIR="${marker_root}" \
  NODE_EXPORTER_TEXTFILE_DIR="${metrics_root}" \
  MOCK_STATE="${mock_state}" \
  MOCK_LOG="${mock_log}" \
  SERVICE_RESULT=signal \
  bash "${script_dir}/backup-worker-recovery.sh"
test "$(cat "${mock_state}")" = running
test ! -e "${marker_root}/worker-restart-required"
grep -qx 'image_studio_backup_last_run_success 0' "${metrics_root}/cornfield_backup.prom"

if command -v flock >/dev/null 2>&1; then
  setup_case concurrent
  mkdir -p "${backup_stage}/database"
  chmod 0700 "${backup_stage}/database"
  printf '%s\n' exited > "${mock_state}"
  export STUDIO_ROOT="${studio_root}"
  export BACKUP_WORKER_MARKER_DIR="${marker_root}"
  mark_worker_restart_required "${worker_id}"
  exec 8>"${backup_stage}/database/.maintenance.lock"
  flock -n 8
  if run_backup; then
    echo "concurrent backup acquired an already-held maintenance lock" >&2
    exit 1
  fi
  test ! -s "${mock_log}"
  test "$(cat "${mock_state}")" = exited
  test -f "${marker_root}/worker-restart-required"
  exec 8>&-
fi

setup_case restore-exec-stop-post
mkdir -p "${backup_stage}/database"
chmod 0700 "${backup_stage}/database"
restore_root="${case_root}/restore"
mkdir -p "${restore_root}"
restore_run_id=0123456789abcdef0123456789abcdef
restore_container="cornfield-restore-${restore_run_id}"
restore_target="${restore_root}/${restore_container}"
export BACKUP_STAGE="${backup_stage}"
export RESTORE_CHECK_ROOT="${restore_root}"
install -d -m 0700 "${restore_target}"
write_restore_check_marker "${restore_container}" "${restore_target}"
env \
  PATH="${stub_directory}:${PATH}" \
  BACKUP_STAGE="${backup_stage}" \
  RESTORE_CHECK_ROOT="${restore_root}" \
  NODE_EXPORTER_TEXTFILE_DIR="${metrics_root}" \
  MOCK_STATE="${mock_state}" \
  MOCK_LOG="${mock_log}" \
  MOCK_RESTORE_RUN_ID="${restore_run_id}" \
  SERVICE_RESULT=signal \
  bash "${script_dir}/restore-check-recovery.sh"
test ! -e "${restore_target}"
test ! -e "${backup_stage}/database/restore-check-active"
grep -qx 'image_studio_restore_check_last_run_success 0' "${metrics_root}/cornfield_restore_check.prom"

echo "maintenance safety tests passed"
