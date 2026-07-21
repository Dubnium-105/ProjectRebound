#!/usr/bin/env bash
set -euo pipefail
backup="${1:?usage: verify-backup.sh FILE.dump.age}"
identity="${BACKUP_AGE_IDENTITY_FILE:?BACKUP_AGE_IDENTITY_FILE is required}"
[[ -f "$backup" && -f "$identity" ]] || { echo "backup or age identity missing" >&2; exit 1; }
command -v age >/dev/null; command -v pg_restore >/dev/null
[[ ! -f "$backup.sha256" ]] || sha256sum --check "$backup.sha256"
temporary="$(mktemp)"; trap 'rm -f -- "$temporary"' EXIT
age --decrypt --identity "$identity" --output "$temporary" "$backup"
pg_restore --list "$temporary" >/dev/null
printf 'BACKUP_VERIFIED %s\n' "$backup"
