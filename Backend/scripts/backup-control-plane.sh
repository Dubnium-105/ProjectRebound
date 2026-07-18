#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
backend_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"
deployment_dir="$backend_dir/deployments/control-plane"
env_file="${CONTROL_PLANE_ENV_FILE:-$deployment_dir/.env}"
backup_dir="${1:-$backend_dir/backups}"
[[ -f "$env_file" ]] || { echo "Missing $env_file" >&2; exit 1; }

mkdir -p "$backup_dir"
chmod 700 "$backup_dir"
database="$(sed -n 's/^POSTGRES_DB=//p' "$env_file" | tail -n 1)"
user="$(sed -n 's/^POSTGRES_USER=//p' "$env_file" | tail -n 1)"
output="$backup_dir/projectrebound-$(date -u +%Y%m%dT%H%M%SZ).dump"
if docker info >/dev/null 2>&1; then docker_cmd=(docker); else docker_cmd=(sudo docker); fi

"${docker_cmd[@]}" compose --env-file "$env_file" -f "$deployment_dir/docker-compose.yaml" \
  exec -T postgres pg_dump -U "$user" -d "$database" -Fc >"$output"
chmod 600 "$output"
"${docker_cmd[@]}" compose --env-file "$env_file" -f "$deployment_dir/docker-compose.yaml" \
  exec -T postgres pg_restore -l <"$output" >/dev/null
printf 'BACKUP_OK %s\n' "$output"
