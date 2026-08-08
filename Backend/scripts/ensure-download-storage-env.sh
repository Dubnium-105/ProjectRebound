#!/usr/bin/env bash
set -euo pipefail

env_file="${1:?control-plane env file is required}"
public_base_url="${2:?public base URL is required}"

[[ "$env_file" =~ ^/[A-Za-z0-9._/-]+$ ]] || { echo "Invalid control-plane env file path" >&2; exit 1; }
[[ -f "$env_file" ]] || { echo "Control-plane env file is missing" >&2; exit 1; }
[[ "$public_base_url" =~ ^https://[A-Za-z0-9.-]+/?$ ]] || {
  echo "A public HTTPS API origin without a path is required to configure local download storage" >&2
  exit 1
}

read_setting() {
  local name="$1"
  sed -n "s/^${name}=//p" "$env_file" | tail -n 1
}

has_setting() {
  grep -q "^${1}=" "$env_file"
}

random_hex() {
  local bytes="$1"
  od -An -N "$bytes" -tx1 /dev/urandom | tr -d ' \n'
}

api_host="${public_base_url#https://}"
api_host="${api_host%/}"
case "$api_host" in
  api.*) base_domain="${api_host#api.}" ;;
  *)
    echo "PUBLIC_BASE_URL must use an api.<domain> hostname for automatic download storage setup" >&2
    exit 1
    ;;
esac
[[ "$base_domain" =~ ^[A-Za-z0-9.-]+$ ]] || { echo "Invalid API base domain" >&2; exit 1; }

admin_site="$(read_setting ADMIN_WEB_SITE)"
[[ -n "$admin_site" ]] || { echo "ADMIN_WEB_SITE is required to configure MinIO CORS" >&2; exit 1; }
case "$admin_site" in
  https://*) admin_origin="${admin_site%/}" ;;
  http://*) admin_origin="${admin_site%/}" ;;
  *) admin_origin="https://${admin_site%/}" ;;
esac
[[ "$admin_origin" =~ ^https?://[A-Za-z0-9.-]+$ ]] || { echo "Invalid ADMIN_WEB_SITE" >&2; exit 1; }

minio_site="$(read_setting MINIO_S3_SITE)"
downloads_site="$(read_setting DOWNLOADS_SITE)"
minio_site="${minio_site:-s3.$base_domain}"
downloads_site="${downloads_site:-downloads.$base_domain}"
[[ "$minio_site" =~ ^[A-Za-z0-9.-]+$ ]] || { echo "Invalid MINIO_S3_SITE" >&2; exit 1; }
[[ "$downloads_site" =~ ^[A-Za-z0-9.-]+$ ]] || { echo "Invalid DOWNLOADS_SITE" >&2; exit 1; }

temporary_file="$(mktemp "${env_file}.downloads.XXXXXX")"
trap 'rm -f "$temporary_file"' EXIT HUP INT TERM
cp "$env_file" "$temporary_file"
added_names=()

append_missing() {
  local name="$1"
  local value="$2"
  if ! has_setting "$name"; then
    printf '%s=%s\n' "$name" "$value" >>"$temporary_file"
    added_names+=("$name")
  fi
}

append_missing MINIO_S3_SITE "$minio_site"
append_missing DOWNLOADS_SITE "$downloads_site"
append_missing MINIO_ROOT_USER "minio-root-$(random_hex 8)"
append_missing MINIO_ROOT_PASSWORD "$(random_hex 32)"
append_missing MINIO_CORS_ALLOWED_ORIGINS "$admin_origin"
append_missing DOWNLOADS_ENABLED true
append_missing DOWNLOAD_S3_ENDPOINT "https://$minio_site"
append_missing DOWNLOAD_S3_REGION us-east-1
append_missing DOWNLOAD_S3_BUCKET project-rebound-downloads
append_missing DOWNLOAD_S3_ACCESS_KEY_ID "downloads-$(random_hex 8)"
append_missing DOWNLOAD_S3_SECRET_ACCESS_KEY "$(random_hex 32)"
append_missing DOWNLOAD_PUBLIC_BASE_URL "https://$downloads_site/project-rebound-downloads"
append_missing DOWNLOAD_ALLOWED_EXTENSIONS exe,msi,zip,7z,pdf,md,txt,docx
append_missing DOWNLOAD_MAX_FILE_BYTES 2147483648
append_missing DOWNLOAD_MULTIPART_THRESHOLD_BYTES 67108864
append_missing DOWNLOAD_PART_SIZE_BYTES 16777216
append_missing DOWNLOAD_UPLOAD_SESSION_TTL_HOURS 24
append_missing DOWNLOAD_PRESIGN_TTL_MINUTES 15
append_missing DOWNLOAD_VERIFICATION_INTERVAL_SECONDS 5

if ((${#added_names[@]} > 0)); then
  chmod 600 "$temporary_file"
  mv "$temporary_file" "$env_file"
  trap - EXIT HUP INT TERM
  printf 'Configured missing local download storage settings in %s: %s\n' \
    "$env_file" "${added_names[*]}"
else
  printf 'Local download storage settings are already configured in %s.\n' "$env_file"
fi
