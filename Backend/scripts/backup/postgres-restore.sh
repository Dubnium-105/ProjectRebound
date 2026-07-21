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
bash "$(dirname "$0")/verify-backup.sh" "$backup"
temporary="$(mktemp)"; trap 'rm -f -- "$temporary"' EXIT
age --decrypt --identity "$BACKUP_AGE_IDENTITY_FILE" --output "$temporary" "$backup"
pg_restore --dbname "$DATABASE_URL" --clean --if-exists --no-owner --no-privileges --single-transaction "$temporary"
psql --dbname "$DATABASE_URL" --set ON_ERROR_STOP=1 <<'SQL'
BEGIN;
UPDATE relay_migrations
SET state = 'FAILED', updated_at = NOW()
WHERE state = 'BINDING';
UPDATE relay_allocations
SET state = 'FAILED', failure_reason = 'DATABASE_RESTORE', closed_at = NOW(), updated_at = NOW()
WHERE state IN ('ALLOCATED', 'BINDING', 'ACTIVE', 'MIGRATING');
UPDATE connections
SET state = 'FAILED', selected_path = NULL, failure_reason = 'DATABASE_RESTORE', closed_at = NOW(), updated_at = NOW()
WHERE state NOT IN ('FAILED', 'EXPIRED', 'CLOSED');
UPDATE p2p_room_members
SET status = 'LEFT', left_at = COALESCE(left_at, NOW())
WHERE status = 'ACTIVE';
UPDATE p2p_rooms
SET state = 'CLOSED', player_count = 0, closed_at = COALESCE(closed_at, NOW()), updated_at = NOW()
WHERE state <> 'CLOSED';
UPDATE relay_nodes
SET state = CASE WHEN state = 'REVOKED' THEN 'REVOKED' ELSE 'OFFLINE' END,
    active_allocations = 0,
    current_egress_bps = 0,
    current_ingress_bps = 0,
    lease_expires_at = NULL,
    drain_deadline = NULL,
    updated_at = NOW();
COMMIT;
SQL
write_restore_success_time
printf 'RESTORE_OK ephemeral rooms, connections, allocations, migrations, and node leases were invalidated; application smoke tests are still required\n'
