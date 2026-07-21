#!/usr/bin/env bash
set -euo pipefail

: "${CONTROL_PLANE_IMAGE:?CONTROL_PLANE_IMAGE is required}"
: "${DATABASE_URL:?DATABASE_URL is required by the encrypted backup step}"
: "${BACKUP_ENCRYPTION_RECIPIENT:?BACKUP_ENCRYPTION_RECIPIENT is required}"
script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
scripts_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"
backend_dir="$(CDPATH= cd -- "$scripts_dir/.." && pwd)"
env_file="${CONTROL_PLANE_ENV_FILE:-$backend_dir/deployments/control-plane/.env}"
compose_file="$backend_dir/deployments/control-plane/docker-compose.yaml"

if docker info >/dev/null 2>&1; then docker_cmd=(docker); elif sudo -n docker info >/dev/null 2>&1; then docker_cmd=(sudo docker); else echo 'Docker unavailable.' >&2; exit 1; fi
compose=("${docker_cmd[@]}" compose --env-file "$env_file" -f "$compose_file")
previous_container="$("${compose[@]}" ps -q control-plane)"
previous_image=""
if [[ -n "$previous_container" ]]; then previous_image="$("${docker_cmd[@]}" inspect -f '{{.Config.Image}}' "$previous_container")"; fi

rollback_needed=1
on_exit() {
  status=$?
  if [[ $status -ne 0 && $rollback_needed -eq 1 && -n "$previous_image" ]]; then
    CONTROL_PLANE_ENV_FILE="$env_file" "$script_dir/rollback.sh" control-plane "$previous_image" || true
  fi
}
trap on_exit EXIT

export BACKUP_DIRECTORY="${BACKUP_DIRECTORY:-$backend_dir/backups/postgres}"
"$scripts_dir/backup/postgres-backup.sh"
"$script_dir/preflight.sh"
DEPLOY_SOURCE=ci "$scripts_dir/deploy-control-plane.sh"
"$scripts_dir/verify-control-plane.sh"

observe_seconds="${RELEASE_OBSERVE_SECONDS:-60}"
admin_port="$(sed -n 's/^CONTROL_PLANE_ADMIN_PORT=//p' "$env_file" | tail -n 1)"; admin_port="${admin_port:-18080}"
deadline=$(( $(date -u +%s) + observe_seconds ))
while [[ "$(date -u +%s)" -lt "$deadline" ]]; do
  metrics="$(curl -fsS --max-time 5 "http://127.0.0.1:$admin_port/internal/metrics")"
  grep -q '^postgres_available 1$' <<<"$metrics" || { echo 'PostgreSQL became unavailable.' >&2; exit 1; }
  grep -q '^redis_available 1$' <<<"$metrics" || { echo 'Redis became unavailable.' >&2; exit 1; }
  sleep 5
done
rollback_needed=0
printf 'CONTROL_PLANE_RELEASE_OK image=%s previous_image=%s\n' "$CONTROL_PLANE_IMAGE" "${previous_image:-none}"
