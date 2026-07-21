#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
backend_dir="$(CDPATH= cd -- "$script_dir/../.." && pwd)"
env_file="${CONTROL_PLANE_ENV_FILE:-$backend_dir/deployments/control-plane/.env}"
compose_file="$backend_dir/deployments/control-plane/docker-compose.yaml"
openapi_file="$backend_dir/api/openapi/openapi.yaml"
image="${CONTROL_PLANE_IMAGE:?CONTROL_PLANE_IMAGE is required}"
schema_version="${EXPECTED_SCHEMA_VERSION:-15}"
backup_dir="${BACKUP_DIRECTORY:-$backend_dir/backups/postgres}"
record_file="${RELEASE_RECORD_FILE:-$backend_dir/release-record.json}"

fail() { printf 'PREFLIGHT_FAILED check=%s\n' "$1" >&2; exit 1; }
pass() { printf 'PREFLIGHT_OK check=%s\n' "$1"; }
env_value() { sed -n "s/^$1=//p" "$env_file" | tail -n 1; }

[[ -f "$env_file" ]] || fail config-file
[[ -f "$compose_file" && -f "$openapi_file" ]] || fail repository-files
if grep -Eq 'CHANGE_ME|example\.com|203\.0\.113\.' "$env_file"; then fail config-placeholders; fi
for key in POSTGRES_DB POSTGRES_USER POSTGRES_PASSWORD REDIS_PASSWORD ACCESS_TOKEN_PRIVATE_KEY_BASE64 \
  RELAY_CA_CERT_PEM_BASE64 RELAY_CA_KEY_PEM_BASE64 RELAY_TOKEN_PRIVATE_KEY_BASE64 \
  UPDATE_CDN_BASE_URL UPDATE_SIGNING_PRIVATE_KEY_BASE64; do
  [[ -n "$(env_value "$key")" ]] || fail "config-$key"
done
pass config

temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM
for key in ACCESS_TOKEN_PRIVATE_KEY_BASE64 RELAY_CA_CERT_PEM_BASE64 RELAY_CA_KEY_PEM_BASE64 \
  RELAY_TOKEN_PRIVATE_KEY_BASE64 UPDATE_SIGNING_PRIVATE_KEY_BASE64; do
  printf '%s' "$(env_value "$key")" | base64 -d >"$temporary_dir/$key" 2>/dev/null || fail "decode-$key"
  [[ -s "$temporary_dir/$key" ]] || fail "empty-$key"
done
grep -q 'BEGIN CERTIFICATE' "$temporary_dir/RELAY_CA_CERT_PEM_BASE64" || fail relay-ca-certificate
grep -q 'PRIVATE KEY' "$temporary_dir/RELAY_CA_KEY_PEM_BASE64" || fail relay-ca-private-key
if command -v openssl >/dev/null 2>&1; then
  openssl x509 -in "$temporary_dir/RELAY_CA_CERT_PEM_BASE64" -noout -checkend 604800 >/dev/null || fail relay-ca-expiry
fi
pass signing-keys-and-relay-ca

[[ "$image" != *:latest ]] || fail floating-image
[[ "$image" =~ ^[A-Za-z0-9._/-]+(@sha256:[0-9a-f]{64}|:(sha-[0-9a-f]{40}|[0-9]+\.[0-9]+\.[0-9]+))$ ]] || fail pinned-image
pass image-reference

grep -q '^openapi: 3\.' "$openapi_file" || fail openapi-schema
if command -v go >/dev/null 2>&1; then
  (cd "$backend_dir" && go test ./api/openapi) >/dev/null || fail openapi-tests
fi
pass openapi

available_kb="$(df -Pk "$backend_dir" | awk 'NR==2 {print $4}')"
required_kb="${PREFLIGHT_MIN_FREE_KB:-2097152}"
[[ "$available_kb" =~ ^[0-9]+$ && "$available_kb" -ge "$required_kb" ]] || fail disk-space
pass disk-space

cdn_url="$(env_value UPDATE_CDN_BASE_URL)"
[[ "$cdn_url" =~ ^https:// ]] || fail object-storage-url
object_probe_url="${PREFLIGHT_OBJECT_STORAGE_PROBE_URL:-$cdn_url}"
curl -fsSI --max-time 10 "$object_probe_url" >/dev/null || fail object-storage
pass object-storage

if [[ "${PREFLIGHT_REQUIRE_BACKUP:-1}" == 1 ]]; then
  latest_backup="$(find "$backup_dir" -maxdepth 1 -type f -name 'projectrebound-*.dump.age' -print 2>/dev/null | sort | tail -n 1)"
  [[ -n "$latest_backup" ]] || fail backup-missing
  now="$(date -u +%s)"; modified="$(stat -c %Y "$latest_backup")"
  [[ $((now - modified)) -le ${PREFLIGHT_BACKUP_MAX_AGE_SECONDS:-90000} ]] || fail backup-stale
  [[ -f "$latest_backup.sha256" ]] || fail backup-checksum
  sha256sum --check "$latest_backup.sha256" >/dev/null || fail backup-verification
fi
pass backup

if docker info >/dev/null 2>&1; then docker_cmd=(docker); elif sudo -n docker info >/dev/null 2>&1; then docker_cmd=(sudo docker); else fail docker; fi
compose=("${docker_cmd[@]}" compose --env-file "$env_file" -f "$compose_file")
"${compose[@]}" config -q || fail compose
"${docker_cmd[@]}" pull "$image" >/dev/null || fail image-pull
image_digest="$("${docker_cmd[@]}" image inspect --format '{{index .RepoDigests 0}}' "$image" 2>/dev/null || true)"
[[ "$image_digest" =~ @sha256:[0-9a-f]{64}$ ]] || fail image-digest
pass image-digest

postgres_id="$("${compose[@]}" ps -q postgres)"
redis_id="$("${compose[@]}" ps -q redis)"
if [[ -z "$postgres_id" || -z "$redis_id" ]]; then
  [[ "${PREFLIGHT_ALLOW_COLD_START:-0}" == 1 ]] || fail dependencies-not-running
else
  db_user="$(env_value POSTGRES_USER)"; db_name="$(env_value POSTGRES_DB)"; redis_password="$(env_value REDIS_PASSWORD)"
  "${compose[@]}" exec -T postgres pg_isready -U "$db_user" -d "$db_name" >/dev/null || fail postgres
  "${compose[@]}" exec -T redis redis-cli -a "$redis_password" --no-auth-warning ping | grep -qx PONG || fail redis
  applied_version="$("${compose[@]}" exec -T postgres psql -At -U "$db_user" -d "$db_name" -c 'SELECT COALESCE(MAX(version),0) FROM schema_migrations' 2>/dev/null || true)"
  [[ "$applied_version" =~ ^[0-9]+$ && "$applied_version" -le "$schema_version" ]] || fail migration-state
fi
pass dependencies-and-migrations

commit="$(git -C "$backend_dir" rev-parse HEAD 2>/dev/null || printf unknown)"
go_version="$(go version 2>/dev/null | awk '{print $3}' || printf unavailable)"
build_time="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
umask 077
printf '{"git_commit":"%s","build_time":"%s","go_version":"%s","image":"%s","image_digest":"%s","relay_protocol_version":2,"database_schema_version":%s}\n' \
  "$commit" "$build_time" "$go_version" "$image" "$image_digest" "$schema_version" >"$record_file"
printf 'PREFLIGHT_COMPLETE record=%s\n' "$record_file"
