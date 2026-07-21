#!/usr/bin/env bash
set -euo pipefail

: "${DATABASE_URL:?DATABASE_URL is required}"
backup_dir="${BACKUP_DIRECTORY:-./backups/postgres}"
recipient="${BACKUP_ENCRYPTION_RECIPIENT:-}"
retention_days="${BACKUP_RETENTION_DAYS:-14}"
metrics_dir="${BACKUP_METRICS_DIRECTORY:-}"
write_backup_status() {
  [[ -n "$metrics_dir" ]] || return 0
  mkdir -p -- "$metrics_dir"
  local status_file="$metrics_dir/projectrebound-backup-status.prom"
  local status_tmp
  status_tmp="$(mktemp "$metrics_dir/.projectrebound-backup-status.XXXXXX")"
  printf '# TYPE projectrebound_backup_last_run_success gauge\nprojectrebound_backup_last_run_success %s\n' "$1" >"$status_tmp"
  mv -f -- "$status_tmp" "$status_file"
}
write_backup_success_time() {
  [[ -n "$metrics_dir" ]] || return 0
  mkdir -p -- "$metrics_dir"
  local success_file="$metrics_dir/projectrebound-backup-success.prom"
  local success_tmp
  success_tmp="$(mktemp "$metrics_dir/.projectrebound-backup-success.XXXXXX")"
  printf '# TYPE projectrebound_backup_last_success_timestamp_seconds gauge\nprojectrebound_backup_last_success_timestamp_seconds %s\n' "$(date -u +%s)" >"$success_tmp"
  mv -f -- "$success_tmp" "$success_file"
}
temporary=""
on_exit() {
  status=$?
  [[ -z "$temporary" ]] || rm -f -- "$temporary"
  if [[ $status -ne 0 ]]; then write_backup_status 0; fi
}
trap on_exit EXIT
mkdir -p -- "$backup_dir"; backup_dir="$(cd "$backup_dir" && pwd)"
[[ "$backup_dir" != / && ${#backup_dir} -gt 8 ]] || { echo "unsafe backup directory" >&2; exit 1; }
[[ -n "$recipient" ]] || { echo "BACKUP_ENCRYPTION_RECIPIENT is required" >&2; exit 1; }
command -v pg_dump >/dev/null; command -v pg_restore >/dev/null; command -v age >/dev/null
umask 077
temporary="$(mktemp "$backup_dir/.projectrebound.XXXXXX.dump")"
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
write_backup_success_time
write_backup_status 1
printf 'BACKUP_OK %s\n' "$output"
