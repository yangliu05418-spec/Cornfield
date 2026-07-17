#!/usr/bin/env bash
set -euo pipefail
umask 077

studio_root="${STUDIO_ROOT:-/opt/internal-image-studio}"
data_root="${DATA_ROOT:?DATA_ROOT must be the absolute asset directory used by Compose and Nginx}"
backup_stage="${BACKUP_STAGE:-/var/backups/internal-image-studio}"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
case "${data_root}" in
  /*) ;;
  *) echo "DATA_ROOT must be absolute" >&2; exit 2 ;;
esac
case "${studio_root}" in
  /*) ;;
  *) echo "STUDIO_ROOT must be absolute" >&2; exit 2 ;;
esac
case "${backup_stage}" in
  /) echo "BACKUP_STAGE must not be the filesystem root" >&2; exit 2 ;;
  /*) ;;
  *) echo "BACKUP_STAGE must be absolute" >&2; exit 2 ;;
esac
test -d "${data_root}/assets"
test -f "${studio_root}/compose.yaml"
mkdir -p "${backup_stage}/database"

cd "${studio_root}"
worker_was_running=false
restart_worker() {
  if [[ "${worker_was_running}" == "true" ]]; then
    docker compose up -d worker >/dev/null
    worker_was_running=false
  fi
}
trap restart_worker EXIT INT TERM

# API uploads only write quarantine files. All durable asset promotion and
# purge happens in Worker, so stopping it gives pg_dump and Restic one coherent
# database/filesystem cut. The trap restores service even if backup fails.
if docker compose ps --status running --services | grep -qx worker; then
  worker_was_running=true
  docker compose stop worker
fi

dump_part="${backup_stage}/database/studio-${timestamp}.dump.part"
dump_final="${backup_stage}/database/studio-${timestamp}.dump"
docker compose exec -T postgres pg_dump --no-password -U studio_bootstrap -d studio --format=custom > "${dump_part}"
mv "${dump_part}" "${dump_final}"

backup_targets=("${dump_final}" "${data_root}" "${studio_root}/config" "${studio_root}/compose.yaml" "${studio_root}/.env")
if test -d "${studio_root}/secrets"; then
  backup_targets+=("${studio_root}/secrets")
fi
restic backup "${backup_targets[@]}"
# The consistent database/filesystem snapshot is sealed at this point. Retention
# maintenance can be slow on a remote repository and must not extend Worker
# downtime after the backup itself has completed.
restart_worker
restic forget --keep-daily 7 --keep-weekly 4 --prune
find "${backup_stage}/database" -maxdepth 1 -type f -name 'studio-*.dump' -mtime +2 -delete
trap - EXIT INT TERM
