#!/usr/bin/env bash
set -euo pipefail
backup="${1:?usage: verify-backup.sh FILE.dump.age}"
identity="${BACKUP_AGE_IDENTITY_FILE:?BACKUP_AGE_IDENTITY_FILE is required}"
metrics_dir="${BACKUP_METRICS_DIRECTORY:-}"
write_verification_status() {
  [[ -n "$metrics_dir" ]] || return 0
  mkdir -p -- "$metrics_dir"
  local target="$metrics_dir/projectrebound-backup-verification.prom"
  local temporary_metric
  temporary_metric="$(mktemp "$metrics_dir/.projectrebound-backup-verification.XXXXXX")"
  printf '# TYPE projectrebound_backup_verification_success gauge\nprojectrebound_backup_verification_success %s\n' "$1" >"$temporary_metric"
  if [[ "$1" = 1 ]]; then
    printf '# TYPE projectrebound_backup_verification_last_success_timestamp_seconds gauge\nprojectrebound_backup_verification_last_success_timestamp_seconds %s\n' "$(date -u +%s)" >>"$temporary_metric"
  fi
  mv -f -- "$temporary_metric" "$target"
}
temporary=""
on_exit() {
  status=$?
  [[ -z "$temporary" ]] || rm -f -- "$temporary"
  if [[ $status -ne 0 ]]; then write_verification_status 0; fi
}
trap on_exit EXIT
[[ -f "$backup" && -f "$identity" ]] || { echo "backup or age identity missing" >&2; exit 1; }
command -v age >/dev/null; command -v pg_restore >/dev/null
[[ ! -f "$backup.sha256" ]] || sha256sum --check "$backup.sha256"
temporary="$(mktemp)"
age --decrypt --identity "$identity" --output "$temporary" "$backup"
pg_restore --list "$temporary" >/dev/null
write_verification_status 1
printf 'BACKUP_VERIFIED %s\n' "$backup"
