#!/usr/bin/env bash
set -euo pipefail

studio_root="${STUDIO_ROOT:-/opt/internal-image-studio}"
cd "${studio_root}"

fail() {
  echo "preflight: $*" >&2
  exit 1
}

test "$(id -u)" = "0" || fail "run preflight as root so runtime identities and the active Nginx process can be verified"
test -f .env || fail ".env is missing"
data_root="$(sed -n 's/^DATA_ROOT=//p' .env | tail -n 1)"
public_url="$(sed -n 's/^APP_PUBLIC_URL=//p' .env | tail -n 1)"
app_env="$(sed -n 's/^APP_ENV=//p' .env | tail -n 1)"
provider_mode="$(sed -n 's/^PROVIDER_MODE=//p' .env | tail -n 1)"
cookie_secure="$(sed -n 's/^SESSION_COOKIE_SECURE=//p' .env | tail -n 1)"

test "${app_env}" = "production" || fail "APP_ENV must be production"
test "${provider_mode}" = "live" || fail "PROVIDER_MODE must be live"
test "${cookie_secure}" = "true" || fail "SESSION_COOKIE_SECURE must be true"

case "${data_root}" in
  /*) ;;
  *) fail "DATA_ROOT must be an absolute host path" ;;
esac
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
provider_callback_path="$(secret_source PROVIDER_CALLBACK_SECRET_SOURCE secrets/provider_callback_secret)"
provider_url_signing_path="$(secret_source PROVIDER_URL_SIGNING_SECRET_SOURCE secrets/provider_url_signing_secret)"
for path in "${legnext_key_path}" "${openrouter_key_path}" "${provider_callback_path}" "${provider_url_signing_path}"; do
  check_secret_mode "${path}"
done
test "$(wc -c < "${provider_callback_path}")" -ge 32 || fail "provider callback secret must be at least 32 bytes"
test "$(wc -c < "${provider_url_signing_path}")" -ge 32 || fail "provider URL signing secret must be at least 32 bytes"

compose_profiles="$(printenv COMPOSE_PROFILES 2>/dev/null || true)"
if test -z "${compose_profiles}"; then
  compose_profiles="$(sed -n 's/^COMPOSE_PROFILES=//p' .env | tail -n 1)"
fi
compose_profiles="$(printf '%s' "${compose_profiles}" | tr -d '[:space:]')"
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
echo "preflight passed"
