#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM

env_file="$temporary_dir/control-plane.env"
cat >"$env_file" <<'EOF'
PUBLIC_API_SITE=api.project-rebound.space
ADMIN_WEB_SITE=admin.project-rebound.space
EXISTING_SETTING=preserved
EOF
chmod 644 "$env_file"

first_output="$(bash "$script_dir/ensure-download-storage-env.sh" \
  "$env_file" https://api.project-rebound.space)"
grep -q 'MINIO_ROOT_PASSWORD' <<<"$first_output"
grep -qx 'MINIO_S3_SITE=s3.project-rebound.space' "$env_file"
grep -qx 'DOWNLOADS_SITE=downloads.project-rebound.space' "$env_file"
grep -qx 'MINIO_CORS_ALLOWED_ORIGINS=https://admin.project-rebound.space' "$env_file"
grep -qx 'DOWNLOAD_S3_ENDPOINT=https://s3.project-rebound.space' "$env_file"
grep -qx 'DOWNLOAD_PUBLIC_BASE_URL=https://downloads.project-rebound.space/project-rebound-downloads' "$env_file"
grep -qx 'EXISTING_SETTING=preserved' "$env_file"
case "$(uname -s)" in
  MINGW*|MSYS*) ;;
  *) test "$(stat -c '%a' "$env_file")" = 600 ;;
esac

root_user="$(sed -n 's/^MINIO_ROOT_USER=//p' "$env_file")"
root_password="$(sed -n 's/^MINIO_ROOT_PASSWORD=//p' "$env_file")"
access_key="$(sed -n 's/^DOWNLOAD_S3_ACCESS_KEY_ID=//p' "$env_file")"
secret_key="$(sed -n 's/^DOWNLOAD_S3_SECRET_ACCESS_KEY=//p' "$env_file")"
[[ "$root_user" =~ ^minio-root-[0-9a-f]{16}$ ]]
[[ "$root_password" =~ ^[0-9a-f]{64}$ ]]
[[ "$access_key" =~ ^downloads-[0-9a-f]{16}$ ]]
[[ "$secret_key" =~ ^[0-9a-f]{64}$ ]]
! grep -Fq "$root_password" <<<"$first_output"
! grep -Fq "$secret_key" <<<"$first_output"

before="$(sha256sum "$env_file")"
second_output="$(bash "$script_dir/ensure-download-storage-env.sh" \
  "$env_file" https://api.project-rebound.space)"
after="$(sha256sum "$env_file")"
test "$before" = "$after"
grep -q 'already configured' <<<"$second_output"

custom_env="$temporary_dir/custom.env"
cat >"$custom_env" <<'EOF'
ADMIN_WEB_SITE="https://console.example.net"
MINIO_S3_SITE='objects.example.net'
DOWNLOADS_SITE="files.example.net"
MINIO_ROOT_USER=existing-root
MINIO_ROOT_PASSWORD=existing-password
DOWNLOAD_S3_ACCESS_KEY_ID=existing-access
DOWNLOAD_S3_SECRET_ACCESS_KEY=existing-secret
EOF
bash "$script_dir/ensure-download-storage-env.sh" \
  "$custom_env" https://api.example.net >/dev/null
grep -qx 'MINIO_ROOT_PASSWORD=existing-password' "$custom_env"
grep -qx 'DOWNLOAD_S3_ENDPOINT=https://objects.example.net' "$custom_env"
grep -qx 'DOWNLOAD_PUBLIC_BASE_URL=https://files.example.net/project-rebound-downloads' "$custom_env"

legacy_env="$temporary_dir/legacy.env"
printf 'ADMIN_WEB_SITE=http://:8081\n' >"$legacy_env"
bash "$script_dir/ensure-download-storage-env.sh" \
  "$legacy_env" https://api.project-rebound.space >/dev/null
grep -qx 'MINIO_CORS_ALLOWED_ORIGINS=https://admin.project-rebound.space' "$legacy_env"

if bash "$script_dir/ensure-download-storage-env.sh" \
  "$env_file" https://project-rebound.space/path >/dev/null 2>&1; then
  echo 'Expected a public URL with a path to be rejected.' >&2
  exit 1
fi

printf 'DOWNLOAD_STORAGE_ENV_TEST_OK\n'
