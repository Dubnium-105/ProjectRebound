#!/usr/bin/env bash
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL is required}"
backup_dir="${BACKUP_DIRECTORY:-./backups/postgres}"
recipient="${BACKUP_ENCRYPTION_RECIPIENT:-}"
retention_days="${BACKUP_RETENTION_DAYS:-14}"
mkdir -p -- "$backup_dir"; backup_dir="$(cd "$backup_dir" && pwd)"
[[ "$backup_dir" != / && ${#backup_dir} -gt 8 ]] || { echo "unsafe backup directory" >&2; exit 1; }
[[ -n "$recipient" ]] || { echo "BACKUP_ENCRYPTION_RECIPIENT is required" >&2; exit 1; }
command -v pg_dump >/dev/null; command -v pg_restore >/dev/null; command -v age >/dev/null
umask 077
temporary="$(mktemp "$backup_dir/.projectrebound.XXXXXX.dump")"
trap 'rm -f -- "$temporary"' EXIT
stamp="$(date -u +%Y%m%dT%H%M%SZ)"; output="$backup_dir/projectrebound-$stamp.dump.age"
pg_dump --dbname "$DATABASE_URL" --format custom --compress 9 --no-owner --no-privileges --file "$temporary"
pg_restore --list "$temporary" >/dev/null
age --recipient "$recipient" --output "$output" "$temporary"
sha256sum "$output" >"$output.sha256"
if [[ -n "${BACKUP_RCLONE_REMOTE:-}" ]]; then
  command -v rclone >/dev/null
  rclone copyto "$output" "${BACKUP_RCLONE_REMOTE%/}/$(basename "$output")"
  rclone copyto "$output.sha256" "${BACKUP_RCLONE_REMOTE%/}/$(basename "$output.sha256")"
fi
find "$backup_dir" -maxdepth 1 -type f \( -name 'projectrebound-*.dump.age' -o -name 'projectrebound-*.dump.age.sha256' \) -mtime "+$retention_days" -delete
printf 'BACKUP_OK %s\n' "$output"
