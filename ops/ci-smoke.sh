#!/usr/bin/env bash
set -euo pipefail

report_error() {
  local status=$?
  echo "::error file=ops/ci-smoke.sh,line=${BASH_LINENO[0]},title=Fresh Compose smoke failed::command exited with status ${status}" >&2
  return "${status}"
}
trap report_error ERR

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
DATA_ROOT="$(mktemp -d /tmp/cornfield-ci-data.XXXXXX)"
export DATA_ROOT
secret_root="$(mktemp -d /tmp/cornfield-ci-secrets.XXXXXX)"
export POSTGRES_BOOTSTRAP_PASSWORD_SECRET_SOURCE="${secret_root}/postgres_bootstrap_password"
export POSTGRES_OWNER_PASSWORD_SECRET_SOURCE="${secret_root}/postgres_owner_password"
export POSTGRES_API_PASSWORD_SECRET_SOURCE="${secret_root}/postgres_api_password"
export POSTGRES_WORKER_PASSWORD_SECRET_SOURCE="${secret_root}/postgres_worker_password"
export LEGNEXT_API_KEY_SECRET_SOURCE="${secret_root}/legnext_api_key"
export OPENROUTER_API_KEY_SECRET_SOURCE="${secret_root}/openrouter_api_key"
export BFL_API_KEY_SECRET_SOURCE="${secret_root}/bfl_api_key"
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
  local status=$?
  if (( status != 0 )); then
    echo "ci-smoke: failure diagnostics" >&2
    docker compose ps --all >&2 || true
    docker compose logs --no-color --tail=300 >&2 || true
  fi
  docker compose down --volumes --remove-orphans >/dev/null 2>&1 || true
  remove_ci_tmp_dir "${tmp_dir}" cornfield-ci-http
  remove_ci_tmp_dir "${secret_root}" cornfield-ci-secrets
  remove_ci_tmp_dir "${DATA_ROOT}" cornfield-ci-data
  return "${status}"
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
printf '%s' 'ci-bfl-key-000000' > "${BFL_API_KEY_SECRET_SOURCE}"
printf '%s' 'ci-callback-secret-00000000000000000000000000000000' > "${PROVIDER_CALLBACK_SECRET_SOURCE}"
printf '%s' 'ci-provider-url-secret-000000000000000000000000000000' > "${PROVIDER_URL_SIGNING_SECRET_SOURCE}"
chmod 0600 \
  "${POSTGRES_BOOTSTRAP_PASSWORD_SECRET_SOURCE}" "${POSTGRES_OWNER_PASSWORD_SECRET_SOURCE}" \
  "${POSTGRES_API_PASSWORD_SECRET_SOURCE}" "${POSTGRES_WORKER_PASSWORD_SECRET_SOURCE}" \
  "${LEGNEXT_API_KEY_SECRET_SOURCE}" "${OPENROUTER_API_KEY_SECRET_SOURCE}" "${BFL_API_KEY_SECRET_SOURCE}" \
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
  "SELECT has_schema_privilege('studio_api','public','USAGE') AND NOT has_schema_privilege('studio_api','public','CREATE') AND NOT has_table_privilege('studio_api','river_job','SELECT') AND NOT has_table_privilege('studio_api','assets','UPDATE') AND has_column_privilege('studio_api','assets','lock_guard','UPDATE') AND has_column_privilege('studio_api','assets','purge_pending','UPDATE') AND has_column_privilege('studio_api','providers','state','UPDATE') AND NOT has_column_privilege('studio_api','providers','enabled','UPDATE') AND has_column_privilege('studio_worker','user_sessions','expires_at','SELECT') AND NOT has_column_privilege('studio_worker','user_sessions','token_hash','SELECT') AND has_column_privilege('studio_worker','users','id','SELECT') AND has_column_privilege('studio_worker','users','status','SELECT') AND NOT has_column_privilege('studio_worker','users','username','SELECT') AND has_table_privilege('studio_worker','river_job','SELECT') AND has_table_privilege('studio_worker','river_job','INSERT') AND has_table_privilege('studio_worker','river_job','UPDATE') AND has_table_privilege('studio_worker','river_job','DELETE')")"
test "${runtime_privileges}" = "t"
bfl_display_name="$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -Atc \
  "SELECT display_name FROM providers WHERE id='bfl'")"
test "${bfl_display_name}" = "Black Forest Labs"

admin_password='ci-smoke-password-123456'
printf '%s\n' "${admin_password}" | docker compose run --rm -T --no-deps model-apply adminctl \
  --username ci-admin --display-name 'CI Admin'

# Reproduce the production deletion failure that originally left bytes behind:
# the asset is already tombstoned, but its canonical directory still exists.
delete_content='ci-delete-me'
delete_digest="$(printf '%s' "${delete_content}" | sha256sum | cut -d' ' -f1)"
delete_key="${delete_digest:0:2}/${delete_digest:2:2}/${delete_digest}/original.png"
delete_first_directory="${DATA_ROOT}/assets/${delete_digest:0:2}"
delete_second_directory="${delete_first_directory}/${delete_digest:2:2}"
delete_directory="${DATA_ROOT}/assets/${delete_digest:0:2}/${delete_digest:2:2}/${delete_digest}"
sudo install -d -o 65532 -g 65532 -m 0750 \
  "${delete_first_directory}" "${delete_second_directory}" "${delete_directory}"
printf '%s' "${delete_content}" | sudo tee "${delete_directory}/original.png" >/dev/null
printf '%s' 'thumbnail' | sudo tee "${delete_directory}/thumb-320.webp" >/dev/null
sudo chown -R 65532:65532 "${delete_directory}"
test "$(sudo sha256sum "${delete_directory}/original.png" | cut -d' ' -f1)" = "${delete_digest}"
sudo touch -d '10 minutes ago' \
  "${delete_directory}/original.png" "${delete_directory}/thumb-320.webp" "${delete_directory}"
delete_quarantine_key='ci-delete-upload.png'
printf '%s' "${delete_content}" | sudo tee "${DATA_ROOT}/uploads/quarantine/${delete_quarantine_key}" >/dev/null
sudo chown 65532:65532 "${DATA_ROOT}/uploads/quarantine/${delete_quarantine_key}"
deletion_ids="$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -At -F ' ' -v ON_ERROR_STOP=1 -c \
  "WITH target AS (
     INSERT INTO users(username,display_name,password_hash,role,status,must_change_password)
     VALUES('ci-delete-user','Delete User','unused','member','deleting',false) RETURNING id
   ), asset AS (
     INSERT INTO assets(owner_user_id,kind,storage_key,sha256,media_type,width,height,byte_size,purged_at)
     SELECT id,'upload','${delete_key}','${delete_digest}','image/png',1,1,${#delete_content},now() FROM target
   ), upload AS (
     INSERT INTO upload_sessions(owner_user_id,status,original_filename,declared_media_type,declared_size,quarantine_key)
     SELECT id,'validating','delete.png','image/png',${#delete_content},'${delete_quarantine_key}' FROM target
   ), request AS (
     INSERT INTO deletion_requests(kind,owner_user_id,target_user_id,requested_by)
     SELECT 'user',target.id,target.id,admin.id FROM target CROSS JOIN users admin WHERE admin.username='ci-admin'
     RETURNING id
   ) SELECT target.id,request.id FROM target CROSS JOIN request")"
read -r deletion_user_id deletion_request_id <<< "${deletion_ids}"
for _ in $(seq 1 30); do
  deletion_status="$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -Atc \
    "SELECT status FROM deletion_requests WHERE id='${deletion_request_id}'::uuid")"
  [[ "${deletion_status}" == "succeeded" ]] && break
  sleep 1
done
if [[ "${deletion_status}" != "succeeded" ]]; then
  deletion_diagnostic="$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -At -F ' ' -c \
    "SELECT status,attempt_count,COALESCE(error_code,'-'),COALESCE(error_message,'-') FROM deletion_requests WHERE id='${deletion_request_id}'::uuid")"
  echo "::error file=ops/ci-smoke.sh,title=User deletion did not complete::${deletion_diagnostic}" >&2
  sudo find "${delete_second_directory}" -maxdepth 2 -printf 'delete fixture: %f %u:%g %m %T@\n' >&2 || true
  sudo sha256sum "${delete_directory}/original.png" >&2 || true
  false
fi
test "$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -Atc \
  "SELECT status FROM users WHERE id='${deletion_user_id}'::uuid")" = "deleted"
test "$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -Atc \
  "SELECT count(*) FROM assets WHERE owner_user_id='${deletion_user_id}'::uuid")" = "0"
test ! -e "${delete_directory}"
test ! -e "${DATA_ROOT}/uploads/quarantine/${delete_quarantine_key}"

tmp_dir="$(mktemp -d /tmp/cornfield-ci-http.XXXXXX)"
cookie_jar="${tmp_dir}/cookies"
login_json="${tmp_dir}/login.json"
curl --fail-with-body --silent --show-error \
  --cookie-jar "${cookie_jar}" \
  --header 'Content-Type: application/json' \
  --data "{\"username\":\"ci-admin\",\"password\":\"${admin_password}\"}" \
  http://127.0.0.1:8081/api/v1/auth/login > "${login_json}"
csrf_token="$(jq -er '.csrf_token' "${login_json}")"

# A fail-closed provider pause must survive healthy balance/key probes and may
# only be cleared by the authenticated, CSRF-protected, audited admin action.
docker compose exec -T postgres psql -U studio_bootstrap -d studio -v ON_ERROR_STOP=1 -c \
  "UPDATE providers SET state='paused',last_error_code='CI_PERMISSION_DENIED',last_error_at=now() WHERE id='legnext'" >/dev/null
providers_json="${tmp_dir}/providers.json"
curl --fail-with-body --silent --show-error --cookie "${cookie_jar}" \
  http://127.0.0.1:8081/api/v1/admin/providers > "${providers_json}"
jq -e '.items[] | select(.id == "legnext") | .state == "paused" and .last_error_code == "CI_PERMISSION_DENIED"' "${providers_json}" >/dev/null
resume_json="${tmp_dir}/provider-resume.json"
curl --fail-with-body --silent --show-error --cookie "${cookie_jar}" \
  --header "X-CSRF-Token: ${csrf_token}" --request POST \
  http://127.0.0.1:8081/api/v1/admin/providers/legnext/resume > "${resume_json}"
jq -e '.id == "legnext" and .state == "degraded" and .resumed == true' "${resume_json}" >/dev/null
resume_audit_count="$(docker compose exec -T postgres psql -U studio_bootstrap -d studio -Atc \
  "SELECT count(*) FROM audit_logs WHERE action='provider.resume' AND target_type='provider' AND target_id='legnext'")"
test "${resume_audit_count}" = "1"

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
  '{model_id:"openrouter-gemini-3-1-flash-lite-image",capability_revision:$revision,prompt:"A quiet cornfield at blue hour",aspect_ratio:"1:1",resolution:"1K",draw_count:1,input_asset_ids:[$asset]}')"
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
