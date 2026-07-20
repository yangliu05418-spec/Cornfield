#!/usr/bin/env bash
set -euo pipefail

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
# shellcheck source=ops/path-safety.sh
source "${script_dir}/path-safety.sh"

studio_root="${STUDIO_ROOT:-/opt/internal-image-studio}"
require_canonical_directory "${studio_root}" STUDIO_ROOT
cd "${studio_root}"

fail() {
  echo "preflight: $*" >&2
  exit 1
}

test "$(id -u)" = "0" || fail "run preflight as root so runtime identities and the active Nginx process can be verified"
test "${script_dir}" = "${studio_root}/ops" || fail "preflight must run from STUDIO_ROOT/ops"
require_root_owned_path "${studio_root}" STUDIO_ROOT || fail "STUDIO_ROOT and every parent must be root-owned and not group/world writable"

check_trusted_file() {
  path="$1"
  if ! { test -f "${path}" && test ! -L "${path}"; }; then
    fail "${path} must be a regular file"
  fi
  mode="$(stat -c '%a' "${path}")"
  if test "$(stat -c '%u' "${path}")" != "0" || (( (8#${mode} & 0022) != 0 )); then
    fail "${path} must be root-owned and not group/world writable"
  fi
}

check_trusted_file .env
check_trusted_file compose.yaml
for tree in ops config; do
  if ! { test -d "${tree}" && test ! -L "${tree}"; }; then
    fail "${tree} must be a regular directory"
  fi
  unsafe_trusted_entry="$(find "${tree}" -xdev \( ! -uid 0 -o -perm /022 -o \( ! -type d ! -type f \) \) -print -quit)"
  test -z "${unsafe_trusted_entry}" || fail "root maintenance trust boundary contains an unsafe entry: ${unsafe_trusted_entry}"
done
non_executable_script="$(find ops -xdev -type f -name '*.sh' ! -perm -u=x -print -quit)"
test -z "${non_executable_script}" || fail "maintenance script is not executable: ${non_executable_script}"
for override in compose.override.yaml compose.override.yml docker-compose.override.yaml docker-compose.override.yml; do
  if ! { test ! -e "${override}" && test ! -L "${override}"; }; then
    fail "unsupported automatic Compose override is present: ${override}"
  fi
done

data_root="$(sed -n 's/^DATA_ROOT=//p' .env | tail -n 1)"
public_url="$(sed -n 's/^APP_PUBLIC_URL=//p' .env | tail -n 1)"
app_env="$(sed -n 's/^APP_ENV=//p' .env | tail -n 1)"
provider_mode="$(sed -n 's/^PROVIDER_MODE=//p' .env | tail -n 1)"
cookie_secure="$(sed -n 's/^SESSION_COOKIE_SECURE=//p' .env | tail -n 1)"

test "${app_env}" = "production" || fail "APP_ENV must be production"
test "${provider_mode}" = "live" || fail "PROVIDER_MODE must be live"
test "${cookie_secure}" = "true" || fail "SESSION_COOKIE_SECURE must be true"

require_canonical_directory "${data_root}" DATA_ROOT || fail "DATA_ROOT must be an existing canonical directory without symlink components"
case "${public_url}" in
  https://*) ;;
  *) fail "APP_PUBLIC_URL must use HTTPS" ;;
esac
[[ "${public_url}" =~ ^https://[A-Za-z0-9.-]+(:443)?/?$ ]] || fail "APP_PUBLIC_URL must be a bare HTTPS origin on standard port 443 without credentials, path, query, or fragment"

runtime_uid=65532
runtime_gid=65532
command -v setpriv >/dev/null 2>&1 || fail "setpriv is required to verify UID 65532 storage access"
test -x /usr/bin/test || fail "/usr/bin/test is required to verify runtime storage access"

check_data_directory() {
  path="$1"
  expected_mode="$2"
  test -d "${path}" || fail "${path} does not exist"
  owner_uid="$(stat -c '%u' "${path}")"
  owner_gid="$(stat -c '%g' "${path}")"
  mode="$(stat -c '%a' "${path}")"
  test "${owner_uid}" = "${runtime_uid}" || fail "${path} must be owned by UID ${runtime_uid} (found ${owner_uid})"
  test "${owner_gid}" = "${runtime_gid}" || fail "${path} must be owned by GID ${runtime_gid} (found ${owner_gid})"
  test "${mode}" = "${expected_mode}" || fail "${path} must have mode 0${expected_mode} (found ${mode})"
  setpriv --reuid="${runtime_uid}" --regid="${runtime_gid}" --clear-groups /usr/bin/test -x "${path}" || fail "UID ${runtime_uid} cannot traverse ${path}"
  setpriv --reuid="${runtime_uid}" --regid="${runtime_gid}" --clear-groups /usr/bin/test -w "${path}" || fail "UID ${runtime_uid} cannot write ${path}"
}

check_data_directory "${data_root}" 750
check_data_directory "${data_root}/assets" 750
check_data_directory "${data_root}/uploads" 700
check_data_directory "${data_root}/uploads/tmp" 700
check_data_directory "${data_root}/uploads/quarantine" 700

unsafe_asset_entry="$(find "${data_root}/assets" -xdev ! -type d ! -type f -print -quit)"
test -z "${unsafe_asset_entry}" || fail "asset tree contains a symlink or special file: ${unsafe_asset_entry}"
unsafe_asset_directory="$(find "${data_root}/assets" -xdev -type d ! \( -uid "${runtime_uid}" -a -gid "${runtime_gid}" -a -perm 0750 \) -print -quit)"
test -z "${unsafe_asset_directory}" || fail "asset directory must be UID/GID ${runtime_uid}:${runtime_gid} with mode 0750: ${unsafe_asset_directory}"
unsafe_asset_file="$(find "${data_root}/assets" -xdev -type f ! \( -uid "${runtime_uid}" -a -gid "${runtime_gid}" -a \( -perm 0640 -o -perm 0644 \) \) -print -quit)"
test -z "${unsafe_asset_file}" || fail "asset file must be UID/GID ${runtime_uid}:${runtime_gid} with mode 0640 or 0644: ${unsafe_asset_file}"

secret_source() {
  variable="$1"
  fallback="$2"
  value="$(printenv "${variable}" 2>/dev/null || true)"
  if test -z "${value}"; then
    value="$(sed -n "s#^${variable}=##p" .env | tail -n 1)"
  fi
  printf '%s' "${value:-${fallback}}"
}

check_secret_mode() {
  path="$1"
  expected_uid="${2:-65532}"
  test -s "${path}" || fail "${path} is missing or empty"
  mode="$(stat -c '%a' "${path}")"
  test "${mode}" = "600" || fail "${path} must have mode 0600 (found ${mode})"
  owner_uid="$(stat -c '%u' "${path}")"
  test "${owner_uid}" = "${expected_uid}" || fail "${path} must be owned by runtime UID ${expected_uid} (found ${owner_uid})"
}

bootstrap_password_path="$(secret_source POSTGRES_BOOTSTRAP_PASSWORD_SECRET_SOURCE secrets/postgres_bootstrap_password)"
owner_password_path="$(secret_source POSTGRES_OWNER_PASSWORD_SECRET_SOURCE secrets/postgres_owner_password)"
api_password_path="$(secret_source POSTGRES_API_PASSWORD_SECRET_SOURCE secrets/postgres_api_password)"
worker_password_path="$(secret_source POSTGRES_WORKER_PASSWORD_SECRET_SOURCE secrets/postgres_worker_password)"

check_database_password() {
  path="$1"
  check_secret_mode "${path}"
  awk 'NR == 1 && length($0) >= 32 && $0 !~ /[[:space:]]/ { valid=1 } END { exit !(NR == 1 && valid) }' \
    "${path}" || fail "${path} must contain one password of at least 32 characters without whitespace"
}

check_database_password "${bootstrap_password_path}"
check_database_password "${owner_password_path}"
check_database_password "${api_password_path}"
check_database_password "${worker_password_path}"

release_require_digests="$(printenv RELEASE_REQUIRE_DIGESTS 2>/dev/null || true)"
if test -z "${release_require_digests}"; then
  release_require_digests="$(sed -n 's/^RELEASE_REQUIRE_DIGESTS=//p' .env | tail -n 1)"
fi
release_require_digests="${release_require_digests:-false}"
case "${release_require_digests}" in
  true|false) ;;
  *) fail "RELEASE_REQUIRE_DIGESTS must be true or false" ;;
esac
if test "${release_require_digests}" = "true"; then
  for variable in API_IMAGE WORKER_IMAGE TOOLS_IMAGE WEB_IMAGE; do
    image="$(printenv "${variable}" 2>/dev/null || true)"
    if test -z "${image}"; then
      image="$(sed -n "s#^${variable}=##p" .env | tail -n 1)"
    fi
    [[ "${image}" =~ @sha256:[0-9a-f]{64}$ ]] || fail "${variable} must be pinned by @sha256 when RELEASE_REQUIRE_DIGESTS=true"
  done
fi

legnext_key_path="$(secret_source LEGNEXT_API_KEY_SECRET_SOURCE secrets/legnext_api_key)"
openrouter_key_path="$(secret_source OPENROUTER_API_KEY_SECRET_SOURCE secrets/openrouter_api_key)"
bfl_key_path="$(secret_source BFL_API_KEY_SECRET_SOURCE secrets/bfl_api_key)"
provider_callback_path="$(secret_source PROVIDER_CALLBACK_SECRET_SOURCE secrets/provider_callback_secret)"
provider_url_signing_path="$(secret_source PROVIDER_URL_SIGNING_SECRET_SOURCE secrets/provider_url_signing_secret)"

check_raw_secret() {
  path="$1"
  minimum_length="$2"
  check_secret_mode "${path}"
  awk -v minimum_length="${minimum_length}" \
    'NR == 1 && length($0) >= minimum_length && $0 !~ /[[:space:]]/ { valid=1 } END { exit !(NR == 1 && valid) }' \
    "${path}" || fail "${path} must contain exactly one raw value of at least ${minimum_length} characters without whitespace"
}

for path in "${legnext_key_path}" "${openrouter_key_path}" "${bfl_key_path}"; do
  check_raw_secret "${path}" 16
done
for path in "${provider_callback_path}" "${provider_url_signing_path}"; do
  check_raw_secret "${path}" 32
done

compose_profiles="$(printenv COMPOSE_PROFILES 2>/dev/null || true)"
if test -z "${compose_profiles}"; then
  compose_profiles="$(sed -n 's/^COMPOSE_PROFILES=//p' .env | tail -n 1)"
fi
compose_profiles="$(printf '%s' "${compose_profiles}" | tr -d '[:space:]')"

node_exporter_textfile_dir="$(printenv NODE_EXPORTER_TEXTFILE_DIR 2>/dev/null || true)"
if test -z "${node_exporter_textfile_dir}"; then
  node_exporter_textfile_dir="$(sed -n 's#^NODE_EXPORTER_TEXTFILE_DIR=##p' .env | tail -n 1)"
fi
node_exporter_textfile_dir="${node_exporter_textfile_dir:-/var/lib/node_exporter/textfile_collector}"
case "${node_exporter_textfile_dir}" in
  /) fail "NODE_EXPORTER_TEXTFILE_DIR must not be the filesystem root" ;;
  /*) ;;
  *) fail "NODE_EXPORTER_TEXTFILE_DIR must be an absolute host path" ;;
esac
[[ "${node_exporter_textfile_dir}" != *[[:space:]:]* ]] || fail "NODE_EXPORTER_TEXTFILE_DIR must not contain whitespace or ':'"
test -d "${node_exporter_textfile_dir}" || fail "node-exporter textfile directory is missing: ${node_exporter_textfile_dir}"
test ! -L "${node_exporter_textfile_dir}" || fail "node-exporter textfile directory must not be a symlink"
test "$(stat -c '%u:%g:%a' "${node_exporter_textfile_dir}")" = "0:0:755" || \
  fail "node-exporter textfile directory must be owned by root:root with mode 0755"
test -w "${node_exporter_textfile_dir}" || fail "node-exporter textfile directory is not writable"
for metric_file in cornfield_backup.prom cornfield_restore_check.prom; do
  metric_path="${node_exporter_textfile_dir}/${metric_file}"
  if test -e "${metric_path}" || test -L "${metric_path}"; then
    if ! { test -f "${metric_path}" && test ! -L "${metric_path}"; }; then
      fail "${metric_path} must be a regular file"
    fi
    test "$(stat -c '%u:%g:%a' "${metric_path}")" = "0:0:644" || \
      fail "${metric_path} must be owned by root:root with mode 0644"
  fi
done

# systemd reads a separate root-only environment file. It is part of the
# production backup boundary, not an optional post-deploy convenience.
backup_environment_file=/etc/internal-image-studio/backup.env
if ! { test -f "${backup_environment_file}" && test ! -L "${backup_environment_file}"; }; then
  fail "${backup_environment_file} must be a regular file"
fi
test "$(stat -c '%u:%g:%a' "${backup_environment_file}")" = "0:0:600" || \
  fail "${backup_environment_file} must be owned by root:root with mode 0600"
backup_textfile_dir="$(sed -n 's#^NODE_EXPORTER_TEXTFILE_DIR=##p' "${backup_environment_file}" | tail -n 1)"
backup_textfile_dir="${backup_textfile_dir:-/var/lib/node_exporter/textfile_collector}"
test "${backup_textfile_dir}" = "${node_exporter_textfile_dir}" || \
  fail "NODE_EXPORTER_TEXTFILE_DIR differs between .env and ${backup_environment_file}"
backup_data_root="$(sed -n 's#^DATA_ROOT=##p' "${backup_environment_file}" | tail -n 1)"
test "${backup_data_root}" = "${data_root}" || fail "DATA_ROOT differs between .env and ${backup_environment_file}"
backup_stage="$(sed -n 's#^BACKUP_STAGE=##p' "${backup_environment_file}" | tail -n 1)"
restore_check_root="$(sed -n 's#^RESTORE_CHECK_ROOT=##p' "${backup_environment_file}" | tail -n 1)"
require_canonical_directory "${backup_stage}" BACKUP_STAGE || fail "BACKUP_STAGE must be canonical and contain no symlink components"
test "$(stat -c '%u:%g:%a' "${backup_stage}")" = "0:0:700" || fail "BACKUP_STAGE must be root:root 0700"
require_canonical_directory "${backup_stage}/database" BACKUP_DATABASE_DIRECTORY || fail "BACKUP_STAGE/database must already exist and be canonical"
test "$(stat -c '%u:%g:%a' "${backup_stage}/database")" = "0:0:700" || \
  fail "BACKUP_STAGE/database must be owned by root:root with mode 0700"
require_canonical_directory "${restore_check_root}" RESTORE_CHECK_ROOT || fail "RESTORE_CHECK_ROOT must be canonical and contain no symlink components"
restore_check_mode="$(stat -c '%a' "${restore_check_root}")"
if test "$(stat -c '%u:%g' "${restore_check_root}")" != "0:0" || (( (8#${restore_check_mode} & 0022) != 0 )); then
  fail "RESTORE_CHECK_ROOT must be root:root and not group/world writable"
fi

command -v systemctl >/dev/null 2>&1 || fail "systemctl is required for backup and restore recovery"
command -v systemd-analyze >/dev/null 2>&1 || fail "systemd-analyze is required to validate maintenance units"
for command_name in flock hostname jq openssl restic timeout; do
  command -v "${command_name}" >/dev/null 2>&1 || fail "${command_name} is required for backup and restore operations"
done
backup_worker_marker_dir="$(sed -n 's#^BACKUP_WORKER_MARKER_DIR=##p' "${backup_environment_file}" | tail -n 1)"
backup_worker_marker_dir="${backup_worker_marker_dir:-/var/lib/internal-image-studio}"
test "${backup_worker_marker_dir}" = "/var/lib/internal-image-studio" || fail "BACKUP_WORKER_MARKER_DIR must use the systemd StateDirectory"
if test -e "${backup_worker_marker_dir}" || test -L "${backup_worker_marker_dir}"; then
  require_canonical_directory "${backup_worker_marker_dir}" BACKUP_WORKER_MARKER_DIR || fail "BACKUP_WORKER_MARKER_DIR is unsafe"
  test "$(stat -c '%u:%g:%a' "${backup_worker_marker_dir}")" = "0:0:700" || fail "BACKUP_WORKER_MARKER_DIR must be root:root 0700"
fi
for unit in \
  internal-image-studio-backup.service \
  internal-image-studio-backup.timer \
  internal-image-studio-maintenance-recovery.service \
  internal-image-studio-restore-check.service \
  internal-image-studio-restore-check.timer; do
  installed_unit="/etc/systemd/system/${unit}"
  reviewed_unit="${studio_root}/ops/systemd/${unit}"
  if ! { test -f "${installed_unit}" && test ! -L "${installed_unit}"; }; then
    fail "${installed_unit} must be an installed regular file"
  fi
  test "$(stat -c '%u:%g:%a' "${installed_unit}")" = "0:0:644" || fail "${installed_unit} must be root:root 0644"
  cmp -s "${reviewed_unit}" "${installed_unit}" || fail "${installed_unit} differs from the reviewed repository unit"
done
systemd-analyze verify \
  /etc/systemd/system/internal-image-studio-backup.service \
  /etc/systemd/system/internal-image-studio-backup.timer \
  /etc/systemd/system/internal-image-studio-maintenance-recovery.service \
  /etc/systemd/system/internal-image-studio-restore-check.service \
  /etc/systemd/system/internal-image-studio-restore-check.timer >/dev/null || fail "maintenance systemd units are invalid"
systemctl is-enabled --quiet internal-image-studio-backup.timer || fail "backup timer must be enabled"
systemctl is-enabled --quiet internal-image-studio-restore-check.timer || fail "restore-check timer must be enabled"
systemctl is-enabled --quiet internal-image-studio-maintenance-recovery.service || fail "boot maintenance recovery service must be enabled"

case ",${compose_profiles}," in
  *,observability,*)
    grafana_admin_path="$(secret_source GRAFANA_ADMIN_PASSWORD_SECRET_SOURCE secrets/grafana_admin_password)"
    check_secret_mode "${grafana_admin_path}" 472
    ;;
esac
case ",${compose_profiles}," in
  *,alerting,*)
    alertmanager_webhook_path="$(secret_source ALERTMANAGER_WEBHOOK_URL_SECRET_SOURCE secrets/alertmanager_webhook_url)"
    check_secret_mode "${alertmanager_webhook_path}" 65534
    awk 'NR == 1 && $0 ~ /^https:\/\// && $0 !~ /[[:space:]]/ { valid=1 } END { exit !(NR == 1 && valid) }' \
      "${alertmanager_webhook_path}" || fail "${alertmanager_webhook_path} must contain exactly one HTTPS webhook URL"
    ;;
esac

nginx_site_config="$(printenv NGINX_SITE_CONFIG 2>/dev/null || true)"
if test -z "${nginx_site_config}"; then
  nginx_site_config="$(sed -n 's#^NGINX_SITE_CONFIG=##p' .env | tail -n 1)"
fi
nginx_site_config="${nginx_site_config:-/etc/nginx/sites-enabled/internal-image-studio.conf}"
case "${nginx_site_config}" in
  /*) ;;
  *) fail "NGINX_SITE_CONFIG must be an absolute path" ;;
esac
test -f "${nginx_site_config}" || fail "installed Nginx site config is missing: ${nginx_site_config}"
cmp -s ops/nginx/internal-image-studio.conf "${nginx_site_config}" || fail "installed Nginx site config differs from the reviewed repository config"

nginx_asset_root="$(sed -n 's#^[[:space:]]*alias \(.*\)/assets/;#\1#p' "${nginx_site_config}" | head -n 1)"
test "${nginx_asset_root}" = "${data_root}" || fail "Nginx asset root (${nginx_asset_root}) differs from DATA_ROOT (${data_root})"
public_authority="${public_url#https://}"
public_authority="${public_authority%/}"
public_host="${public_authority%%:*}"
nginx_server_name="$(sed -n 's/^[[:space:]]*server_name[[:space:]]\+\([^;[:space:]]\+\);.*/\1/p' "${nginx_site_config}" | head -n 1)"
test "${nginx_server_name}" = "${public_host}" || fail "Nginx server_name (${nginx_server_name}) differs from APP_PUBLIC_URL host (${public_host})"
certificate_file="$(sed -n 's/^[[:space:]]*ssl_certificate[[:space:]]\+\([^;]*\);.*/\1/p' "${nginx_site_config}" | head -n 1)"
certificate_key="$(sed -n 's/^[[:space:]]*ssl_certificate_key[[:space:]]\+\([^;]*\);.*/\1/p' "${nginx_site_config}" | head -n 1)"
test -r "${certificate_file}" || fail "Nginx TLS certificate is not readable: ${certificate_file}"
test -r "${certificate_key}" || fail "Nginx TLS private key is not readable: ${certificate_key}"
if grep -E "script-src[^;]*'unsafe-inline'" "${nginx_site_config}" >/dev/null; then
  fail "Nginx CSP must not allow inline scripts"
fi
grep -q 'externalize-inline-scripts.mjs' web/package.json || fail "web build does not enforce static CSP hardening"

command -v nginx >/dev/null 2>&1 || fail "host Nginx is not installed"
nginx -t >/dev/null 2>&1 || fail "host Nginx configuration test failed"
nginx_dump="$(nginx -T 2>&1)" || fail "host Nginx configuration dump failed"
nginx_site_resolved="$(readlink -f "${nginx_site_config}")"
if ! grep -Fq "# configuration file ${nginx_site_config}:" <<< "${nginx_dump}" && \
   ! grep -Fq "# configuration file ${nginx_site_resolved}:" <<< "${nginx_dump}"; then
  fail "${nginx_site_config} exists but is not included by the active Nginx configuration"
fi

nginx_worker_user="$(printenv NGINX_WORKER_USER 2>/dev/null || true)"
if test -z "${nginx_worker_user}"; then
  nginx_worker_user="$(sed -n 's/^NGINX_WORKER_USER=//p' .env | tail -n 1)"
fi
nginx_worker_user="${nginx_worker_user:-www-data}"
declared_nginx_user="$(printf '%s\n' "${nginx_dump}" | sed -n 's/^[[:space:]]*user[[:space:]]\+\([^;[:space:]]\+\).*;/\1/p' | head -n 1)"
test "${declared_nginx_user}" = "${nginx_worker_user}" || fail "active Nginx worker user (${declared_nginx_user:-unset}) differs from NGINX_WORKER_USER (${nginx_worker_user})"
id "${nginx_worker_user}" >/dev/null 2>&1 || fail "Nginx worker account does not exist: ${nginx_worker_user}"
getent group "${runtime_gid}" >/dev/null || fail "host group GID ${runtime_gid} is missing"
nginx_worker_groups=" $(id -G "${nginx_worker_user}") "
case "${nginx_worker_groups}" in
  *" ${runtime_gid} "*) ;;
  *) fail "${nginx_worker_user} is not a member of runtime GID ${runtime_gid}" ;;
esac
command -v runuser >/dev/null 2>&1 || fail "runuser is required to verify Nginx asset permissions"
runuser -u "${nginx_worker_user}" -- /usr/bin/test -r "${data_root}/assets" || fail "Nginx worker cannot list the asset directory"
runuser -u "${nginx_worker_user}" -- /usr/bin/test -x "${data_root}/assets" || fail "Nginx worker cannot traverse the asset directory"
asset_sample="$(find "${data_root}/assets" -xdev -type f -print -quit)"
if test -n "${asset_sample}"; then
  runuser -u "${nginx_worker_user}" -- /usr/bin/test -r "${asset_sample}" || fail "Nginx worker cannot read asset sample ${asset_sample}"
fi

command -v pgrep >/dev/null 2>&1 || fail "pgrep is required to verify active Nginx workers"
nginx_worker_uid="$(id -u "${nginx_worker_user}")"
nginx_worker_pids="$(pgrep -x -u "${nginx_worker_uid}" nginx || true)"
test -n "${nginx_worker_pids}" || fail "no active Nginx worker process is running as ${nginx_worker_user}; restart Nginx after adding the runtime group"
for pid in ${nginx_worker_pids}; do
  awk -v gid="${runtime_gid}" '$1 == "Groups:" { for (i=2; i<=NF; i++) if ($i == gid) found=1 } END { exit !found }' "/proc/${pid}/status" || \
    fail "active Nginx worker ${pid} has not inherited GID ${runtime_gid}; restart Nginx"
done

docker compose config --quiet
docker compose --profile observability config --quiet
echo "preflight passed"
