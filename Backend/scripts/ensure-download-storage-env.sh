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
  local value
  value="$(sed -n "s/^${name}=//p" "$env_file" | tail -n 1)"
  value="${value%$'\r'}"
  if ((${#value} >= 2)); then
    local first="${value:0:1}"
    local last="${value: -1}"
    if [[ ("$first" == '"' && "$last" == '"') || ("$first" == "'" && "$last" == "'") ]]; then
      value="${value:1:${#value}-2}"
    fi
  fi
  printf '%s' "$value"
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
case "$admin_site" in
  https://*) admin_origin="${admin_site%/}" ;;
  http://*)
    admin_authority="${admin_site#http://}"
    admin_authority="${admin_authority%/}"
    admin_origin="https://${admin_authority%%:*}"
    ;;
  *) admin_origin="https://${admin_site%/}" ;;
esac
if [[ ! "$admin_origin" =~ ^https://[A-Za-z0-9.-]+(:[0-9]{1,5})?$ ]]; then
  admin_origin="https://admin.$base_domain"
fi

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
existing_cors="$(read_setting MINIO_CORS_ALLOWED_ORIGINS)"
if [[ -z "$existing_cors" ]]; then
  append_missing MINIO_CORS_ALLOWED_ORIGINS "$admin_origin"
elif [[ "$existing_cors" =~ ^http://([A-Za-z0-9.-]+)(:80)?$ &&
        "$admin_origin" == "https://${BASH_REMATCH[1]}" ]]; then
  sed "s|^MINIO_CORS_ALLOWED_ORIGINS=.*$|MINIO_CORS_ALLOWED_ORIGINS=$admin_origin|" \
    "$temporary_file" >"${temporary_file}.cors"
  mv "${temporary_file}.cors" "$temporary_file"
  added_names+=(MINIO_CORS_ALLOWED_ORIGINS)
fi
append_missing DOWNLOADS_ENABLED true
download_endpoint="$(read_setting DOWNLOAD_S3_ENDPOINT)"
download_upload_endpoint="$(read_setting DOWNLOAD_S3_UPLOAD_ENDPOINT)"
inferred_upload_endpoint=""
if [[ -z "$download_endpoint" ]]; then
  append_missing DOWNLOAD_S3_ENDPOINT "http://minio:9000"
  download_endpoint="http://minio:9000"
  inferred_upload_endpoint="https://$minio_site"
elif [[ "$download_endpoint" == "https://$minio_site" || "$download_endpoint" == "http://$minio_site" ]]; then
  inferred_upload_endpoint="$download_endpoint"
  sed "s|^DOWNLOAD_S3_ENDPOINT=.*$|DOWNLOAD_S3_ENDPOINT=http://minio:9000|" \
    "$temporary_file" >"${temporary_file}.endpoint"
  mv "${temporary_file}.endpoint" "$temporary_file"
  added_names+=(DOWNLOAD_S3_ENDPOINT)
  download_endpoint="http://minio:9000"
elif [[ "$download_endpoint" == "http://minio:9000" ]]; then
  inferred_upload_endpoint="https://$minio_site"
fi
if [[ -z "$download_upload_endpoint" ]]; then
  append_missing DOWNLOAD_S3_UPLOAD_ENDPOINT "${inferred_upload_endpoint:-$download_endpoint}"
fi
append_missing DOWNLOAD_S3_REGION us-east-1
download_bucket="$(read_setting DOWNLOAD_S3_BUCKET)"
download_bucket="${download_bucket:-project-rebound-downloads}"
append_missing DOWNLOAD_S3_BUCKET "$download_bucket"
append_missing DOWNLOAD_S3_ACCESS_KEY_ID "downloads-$(random_hex 8)"
append_missing DOWNLOAD_S3_SECRET_ACCESS_KEY "$(random_hex 32)"
download_public_base="$(read_setting DOWNLOAD_PUBLIC_BASE_URL)"
download_public_base="${download_public_base:-https://$downloads_site/$download_bucket}"
append_missing DOWNLOAD_PUBLIC_BASE_URL "$download_public_base"
download_public_probe_base="$(read_setting DOWNLOAD_PUBLIC_PROBE_BASE_URL)"
if [[ -z "$download_public_probe_base" ]]; then
  if [[ "$download_endpoint" == "http://minio:9000" ]]; then
    download_public_probe_base="http://minio:9000/$download_bucket"
  else
    download_public_probe_base="$download_public_base"
  fi
  append_missing DOWNLOAD_PUBLIC_PROBE_BASE_URL "$download_public_probe_base"
fi
download_allowed_extensions="$(read_setting DOWNLOAD_ALLOWED_EXTENSIONS)"
if [[ -z "$download_allowed_extensions" ]]; then
  append_missing DOWNLOAD_ALLOWED_EXTENSIONS exe,msi,zip,7z,pdf,md,txt,docx,json
else
  has_json_extension=false
  IFS=',' read -ra download_extensions <<<"$download_allowed_extensions"
  for extension in "${download_extensions[@]}"; do
    extension="${extension#"${extension%%[![:space:]]*}"}"
    extension="${extension%"${extension##*[![:space:]]}"}"
    extension="${extension#.}"
    if [[ "${extension,,}" == "json" ]]; then
      has_json_extension=true
      break
    fi
  done
  if [[ "$has_json_extension" == false ]]; then
    download_allowed_extensions="${download_allowed_extensions%,},json"
    sed "s|^DOWNLOAD_ALLOWED_EXTENSIONS=.*$|DOWNLOAD_ALLOWED_EXTENSIONS=$download_allowed_extensions|" \
      "$temporary_file" >"${temporary_file}.extensions"
    mv "${temporary_file}.extensions" "$temporary_file"
    added_names+=(DOWNLOAD_ALLOWED_EXTENSIONS)
  fi
fi
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
