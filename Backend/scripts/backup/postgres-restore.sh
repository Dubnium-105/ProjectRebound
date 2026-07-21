#!/usr/bin/env bash
set -euo pipefail
: "${DATABASE_URL:?DATABASE_URL is required}"
: "${BACKUP_AGE_IDENTITY_FILE:?BACKUP_AGE_IDENTITY_FILE is required}"
[[ "${RESTORE_I_UNDERSTAND:-}" = "replace-target-database" ]] || { echo "set RESTORE_I_UNDERSTAND=replace-target-database" >&2; exit 1; }
backup="${1:?usage: postgres-restore.sh FILE.dump.age}"
[[ -f "$backup" ]] || { echo "backup missing" >&2; exit 1; }
"$(dirname "$0")/verify-backup.sh" "$backup"
temporary="$(mktemp)"; trap 'rm -f -- "$temporary"' EXIT
age --decrypt --identity "$BACKUP_AGE_IDENTITY_FILE" --output "$temporary" "$backup"
pg_restore --dbname "$DATABASE_URL" --clean --if-exists --no-owner --no-privileges --single-transaction "$temporary"
printf 'RESTORE_OK migrations and smoke tests are still required\n'
