#!/usr/bin/env bash
set -euo pipefail
: "${DATABASE_URL:?DATABASE_URL is required}"
: "${BACKUP_AGE_IDENTITY_FILE:?BACKUP_AGE_IDENTITY_FILE is required}"
[[ "${RESTORE_I_UNDERSTAND:-}" = "replace-target-database" ]] || { echo "set RESTORE_I_UNDERSTAND=replace-target-database" >&2; exit 1; }
backup="${1:?usage: postgres-restore.sh FILE.dump.age}"
metrics_dir="${BACKUP_METRICS_DIRECTORY:-}"
write_restore_success_time() {
  [[ -n "$metrics_dir" ]] || return 0
  mkdir -p -- "$metrics_dir"
  local target="$metrics_dir/projectrebound-restore-drill.prom"
  local temporary_metric
  temporary_metric="$(mktemp "$metrics_dir/.projectrebound-restore-drill.XXXXXX")"
  printf '# TYPE projectrebound_restore_drill_last_success_timestamp_seconds gauge\nprojectrebound_restore_drill_last_success_timestamp_seconds %s\n' "$(date -u +%s)" >"$temporary_metric"
  mv -f -- "$temporary_metric" "$target"
}
[[ -f "$backup" ]] || { echo "backup missing" >&2; exit 1; }
"$(dirname "$0")/verify-backup.sh" "$backup"
temporary="$(mktemp)"; trap 'rm -f -- "$temporary"' EXIT
age --decrypt --identity "$BACKUP_AGE_IDENTITY_FILE" --output "$temporary" "$backup"
pg_restore --dbname "$DATABASE_URL" --clean --if-exists --no-owner --no-privileges --single-transaction "$temporary"
write_restore_success_time
printf 'RESTORE_OK migrations and smoke tests are still required\n'
