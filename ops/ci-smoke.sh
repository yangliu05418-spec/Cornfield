#!/usr/bin/env bash
set -euo pipefail

if [[ "${CI:-}" != "true" ]]; then
  echo "ci-smoke: refusing to run outside an isolated CI runner" >&2
  exit 1
fi

studio_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "${studio_root}"

export COMPOSE_PROJECT_NAME="cornfield-smoke-${GITHUB_RUN_ID:-0}-${GITHUB_RUN_ATTEMPT:-0}"
export APP_ENV=development
export APP_PUBLIC_URL=http://127.0.0.1:8080
export SESSION_COOKIE_SECURE=false
export PROVIDER_MODE=mock
export DATA_ROOT="$(mktemp -d /tmp/cornfield-ci-data.XXXXXX)"
secret_root="$(mktemp -d /tmp/cornfield-ci-secrets.XXXXXX)"
export POSTGRES_BOOTSTRAP_PASSWORD_SECRET_SOURCE="${secret_root}/postgres_bootstrap_password"
export POSTGRES_OWNER_PASSWORD_SECRET_SOURCE="${secret_root}/postgres_owner_password"
export POSTGRES_API_PASSWORD_SECRET_SOURCE="${secret_root}/postgres_api_password"
export POSTGRES_WORKER_PASSWORD_SECRET_SOURCE="${secret_root}/postgres_worker_password"
export LEGNEXT_API_KEY_SECRET_SOURCE="${secret_root}/legnext_api_key"
export OPENROUTER_API_KEY_SECRET_SOURCE="${secret_root}/openrouter_api_key"
export PROVIDER_CALLBACK_SECRET_SOURCE="${secret_root}/provider_callback_secret"
export PROVIDER_URL_SIGNING_SECRET_SOURCE="${secret_root}/provider_url_signing_secret"
tmp_dir=""

remove_ci_tmp_dir() {
  local path="$1"
  local prefix="$2"
  if [[ -n "${path}" && "${path}" == "/tmp/${prefix}."* && -d "${path}" ]]; then
    sudo rm -rf -- "${path}"
  elif [[ -n "${path}" ]]; then
    echo "ci-smoke: refusing to remove unexpected temporary path: ${path}" >&2
  fi
}

cleanup() {
  docker compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  remove_ci_tmp_dir "${tmp_dir}" cornfield-ci-http
  remove_ci_tmp_dir "${secret_root}" cornfield-ci-secrets
  remove_ci_tmp_dir "${DATA_ROOT}" cornfield-ci-data
}
trap cleanup EXIT

install -d -m 0750 "${DATA_ROOT}" "${DATA_ROOT}/assets"
install -d -m 0700 \
  "${DATA_ROOT}/uploads" \
  "${DATA_ROOT}/uploads/tmp" \
  "${DATA_ROOT}/uploads/quarantine"
sudo chown -R 65532:65532 "${DATA_ROOT}"
test "$(sudo stat -c '%u:%g:%a' "${DATA_ROOT}")" = "65532:65532:750"
test "$(sudo stat -c '%u:%g:%a' "${DATA_ROOT}/assets")" = "65532:65532:750"
test "$(sudo stat -c '%u:%g:%a' "${DATA_ROOT}/uploads")" = "65532:65532:700"
test "$(sudo stat -c '%u:%g:%a' "${DATA_ROOT}/uploads/tmp")" = "65532:65532:700"
test "$(sudo stat -c '%u:%g:%a' "${DATA_ROOT}/uploads/quarantine")" = "65532:65532:700"
printf '%s' 'ci-bootstrap-password-000000000000000000000000' > "${POSTGRES_BOOTSTRAP_PASSWORD_SECRET_SOURCE}"
printf '%s' 'ci-owner-password-0000000000000000000000000000' > "${POSTGRES_OWNER_PASSWORD_SECRET_SOURCE}"
printf '%s' 'ci-api-password-000000000000000000000000000000' > "${POSTGRES_API_PASSWORD_SECRET_SOURCE}"
printf '%s' 'ci-worker-password-0000000000000000000000000000' > "${POSTGRES_WORKER_PASSWORD_SECRET_SOURCE}"
printf '%s' 'ci-legnext-key' > "${LEGNEXT_API_KEY_SECRET_SOURCE}"
printf '%s' 'ci-openrouter-key' > "${OPENROUTER_API_KEY_SECRET_SOURCE}"
printf '%s' 'ci-callback-secret-00000000000000000000000000000000' > "${PROVIDER_CALLBACK_SECRET_SOURCE}"
printf '%s' 'ci-provider-url-secret-000000000000000000000000000000' > "${PROVIDER_URL_SIGNING_SECRET_SOURCE}"
chmod 0600 \
  "${POSTGRES_BOOTSTRAP_PASSWORD_SECRET_SOURCE}" "${POSTGRES_OWNER_PASSWORD_SECRET_SOURCE}" \
  "${POSTGRES_API_PASSWORD_SECRET_SOURCE}" "${POSTGRES_WORKER_PASSWORD_SECRET_SOURCE}" \
  "${LEGNEXT_API_KEY_SECRET_SOURCE}" "${OPENROUTER_API_KEY_SECRET_SOURCE}" \
  "${PROVIDER_CALLBACK_SECRET_SOURCE}" "${PROVIDER_URL_SIGNING_SECRET_SOURCE}"
sudo chown -R 65532:65532 "${secret_root}"

docker compose up -d --build

for _ in $(seq 1 90); do
  if curl --silent --fail http://127.0.0.1:8081/health/ready >/dev/null; then
    break
  fi
  sleep 1
done
curl --fail-with-body http://127.0.0.1:8081/health/ready >/dev/null
curl --fail-with-body http://127.0.0.1:8080/health >/dev/null

role_hardening="$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -Atc \
  "SELECT count(*)=3 AND bool_and(NOT rolsuper AND NOT rolcreatedb AND NOT rolcreaterole AND NOT rolreplication AND NOT rolbypassrls) FROM pg_roles WHERE rolname IN ('studio_owner','studio_api','studio_worker')")"
test "${role_hardening}" = "t"
ownership_hardening="$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -Atc \
  "SELECT (SELECT pg_get_userbyid(datdba) FROM pg_database WHERE datname='studio')='studio_owner' AND NOT EXISTS(SELECT 1 FROM pg_roles WHERE rolname='studio')")"
test "${ownership_hardening}" = "t"
runtime_privileges="$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -Atc \
  "SELECT has_schema_privilege('studio_api','public','USAGE') AND NOT has_schema_privilege('studio_api','public','CREATE') AND NOT has_table_privilege('studio_api','river_job','SELECT') AND has_column_privilege('studio_worker','user_sessions','expires_at','SELECT') AND NOT has_column_privilege('studio_worker','user_sessions','token_hash','SELECT') AND has_table_privilege('studio_worker','river_job','SELECT') AND has_table_privilege('studio_worker','river_job','INSERT') AND has_table_privilege('studio_worker','river_job','UPDATE') AND has_table_privilege('studio_worker','river_job','DELETE')")"
test "${runtime_privileges}" = "t"

admin_password='ci-smoke-password-123456'
printf '%s\n' "${admin_password}" | docker compose run --rm -T --no-deps model-apply adminctl \
  --username ci-admin --display-name 'CI Admin'

tmp_dir="$(mktemp -d /tmp/cornfield-ci-http.XXXXXX)"
cookie_jar="${tmp_dir}/cookies"
login_json="${tmp_dir}/login.json"
curl --fail-with-body --silent --show-error \
  --cookie-jar "${cookie_jar}" \
  --header 'Content-Type: application/json' \
  --data "{\"username\":\"ci-admin\",\"password\":\"${admin_password}\"}" \
  http://127.0.0.1:8081/api/v1/auth/login > "${login_json}"
csrf_token="$(jq -er '.csrf_token' "${login_json}")"

models_json="${tmp_dir}/models.json"
curl --fail-with-body --silent --show-error --cookie "${cookie_jar}" \
  http://127.0.0.1:8081/api/v1/models > "${models_json}"
revision="$(jq -er '.revision' "${models_json}")"
jq -e '.models | length >= 2' "${models_json}" >/dev/null

# Exercise streaming upload, quarantine validation, libvips decode and
# content-addressed promotion before using the asset as an image input.
reference_png="${tmp_dir}/reference.png"
printf '%s' 'iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAFElEQVR4nGP8z8DAwMDAxMDAwMDAAAANHQEDasKb6QAAAABJRU5ErkJggg==' | base64 --decode > "${reference_png}"
reference_size="$(wc -c < "${reference_png}" | tr -d ' ')"
upload_json="${tmp_dir}/upload.json"
curl --fail-with-body --silent --show-error --cookie "${cookie_jar}" \
  --header "X-CSRF-Token: ${csrf_token}" --header 'Content-Type: application/json' \
  --data "{\"filename\":\"reference.png\",\"media_type\":\"image/png\",\"size\":${reference_size}}" \
  http://127.0.0.1:8081/api/v1/uploads > "${upload_json}"
upload_id="$(jq -er '.id' "${upload_json}")"
curl --fail-with-body --silent --show-error --cookie "${cookie_jar}" \
  --header "X-CSRF-Token: ${csrf_token}" --header 'Content-Type: image/png' \
  --request PUT --data-binary "@${reference_png}" \
  "http://127.0.0.1:8081/api/v1/uploads/${upload_id}/content" >/dev/null

upload_status_json="${tmp_dir}/upload-status.json"
for _ in $(seq 1 60); do
  curl --fail-with-body --silent --show-error --cookie "${cookie_jar}" \
    "http://127.0.0.1:8081/api/v1/uploads/${upload_id}" > "${upload_status_json}"
  if [[ "$(jq -r '.status' "${upload_status_json}")" == "ready" ]]; then
    break
  fi
  sleep 1
done
jq -e '.status == "ready" and (.asset_id | type == "string")' "${upload_status_json}" >/dev/null
reference_asset_id="$(jq -er '.asset_id' "${upload_status_json}")"

generation_json="${tmp_dir}/generation.json"
request_json="$(jq -nc \
  --arg revision "${revision}" --arg asset "${reference_asset_id}" \
  '{model_id:"openrouter-gpt-image-1",capability_revision:$revision,prompt:"A quiet cornfield at blue hour",aspect_ratio:"1:1",resolution:"1K",draw_count:1,input_asset_ids:[$asset]}')"
curl --fail-with-body --silent --show-error --cookie "${cookie_jar}" \
  --header "X-CSRF-Token: ${csrf_token}" --header 'Content-Type: application/json' \
  --header 'Idempotency-Key: ci-smoke-image-to-image-v1' --data "${request_json}" \
  http://127.0.0.1:8081/api/v1/generations > "${generation_json}"
batch_id="$(jq -er '.id' "${generation_json}")"

# The same idempotency key and body must resolve to the same batch.
idempotent_json="${tmp_dir}/idempotent.json"
curl --fail-with-body --silent --show-error --cookie "${cookie_jar}" \
  --header "X-CSRF-Token: ${csrf_token}" --header 'Content-Type: application/json' \
  --header 'Idempotency-Key: ci-smoke-image-to-image-v1' --data "${request_json}" \
  http://127.0.0.1:8081/api/v1/generations > "${idempotent_json}"
test "$(jq -er '.id' "${idempotent_json}")" = "${batch_id}"

batch_json="${tmp_dir}/batch.json"
for _ in $(seq 1 120); do
  curl --fail-with-body --silent --show-error --cookie "${cookie_jar}" \
    "http://127.0.0.1:8081/api/v1/generations/${batch_id}" > "${batch_json}"
  if [[ "$(jq -r '.status' "${batch_json}")" == "succeeded" ]]; then
    break
  fi
  sleep 1
done
jq -e '.status == "succeeded" and .completed_outputs == 1 and (.jobs[0].outputs | length) == 1' "${batch_json}" >/dev/null
output_asset_id="$(jq -er '.jobs[0].outputs[0].asset_id' "${batch_json}")"
reference_count="$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -Atc \
  "SELECT usage->>'reference_count' FROM provider_attempts WHERE job_id=(SELECT id FROM generation_jobs WHERE batch_id='${batch_id}'::uuid LIMIT 1) AND operation='submit' ORDER BY id DESC LIMIT 1")"
test "${reference_count}" = "1"

asset_headers="${tmp_dir}/asset.headers"
curl --fail-with-body --silent --show-error --cookie "${cookie_jar}" \
  --dump-header "${asset_headers}" --output /dev/null \
  "http://127.0.0.1:8081/api/v1/assets/${output_asset_id}/content?variant=640"
grep -Eiq '^X-Accel-Redirect: /_protected_assets/' "${asset_headers}"

curl --silent --show-error --max-time 3 --no-buffer --cookie "${cookie_jar}" \
  http://127.0.0.1:8081/api/v1/events > "${tmp_dir}/events" || test "$?" = 28
grep -Eq '^id: [0-9]+' "${tmp_dir}/events"

docker compose ps --status running --services | grep -qx api
docker compose ps --status running --services | grep -qx worker
docker compose ps --status running --services | grep -qx postgres
docker compose ps --status running --services | grep -qx web

echo "ci-smoke: full mock image-to-image path passed"
