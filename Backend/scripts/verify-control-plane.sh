#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
backend_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"
env_file="${CONTROL_PLANE_ENV_FILE:-$backend_dir/deployments/control-plane/.env}"
[[ -f "$env_file" ]] || { echo "Missing $env_file" >&2; exit 1; }

public_port="$(sed -n 's/^PUBLIC_API_HTTP_PORT=//p' "$env_file" | tail -n 1)"
admin_port="$(sed -n 's/^CONTROL_PLANE_ADMIN_PORT=//p' "$env_file" | tail -n 1)"
public_url="${PUBLIC_BASE_URL:-http://127.0.0.1:${public_port:-8080}}"
internal_url="http://127.0.0.1:${admin_port:-18080}"

curl -fsS "$public_url/health/live" | grep -q '"status":"live"'
curl -fsS "$public_url/health/ready" | grep -q '"status":"ready"'
curl -fsS "$public_url/v1/client/config" | grep -q '"api_version":"v1"'
test "$(curl -sS -o /dev/null -w '%{http_code}' "$public_url/internal/metrics")" = "404"
test "$(curl -sS -o /dev/null -w '%{http_code}' "$public_url/v1/admin/players")" = "404"
curl -fsS "$internal_url/internal/metrics" | grep -Eq '^# HELP|^[a-zA-Z_]'
printf 'CONTROL_PLANE_VERIFY_OK public=%s internal=%s\n' "$public_url" "$internal_url"
