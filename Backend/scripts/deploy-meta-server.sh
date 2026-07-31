#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
backend_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"
deployment_dir="$backend_dir/deployments/control-plane"
env_file="${CONTROL_PLANE_ENV_FILE:-$deployment_dir/.env}"
compose_file="$deployment_dir/docker-compose.yaml"
compose_override_file="${CONTROL_PLANE_COMPOSE_OVERRIDE_FILE:-}"
image="${META_SERVER_IMAGE:-}"
deploy_source="${DEPLOY_SOURCE:-auto}"

case "$deploy_source" in
  auto)
    if [[ "$image" =~ ^ghcr\.io/[a-z0-9._/-]+(@sha256:[0-9a-f]{64}|:(sha-[0-9a-f]{40}|[0-9]+\.[0-9]+\.[0-9]+))$ ]]; then
      deploy_source="ci"
    else
      deploy_source="source"
    fi
    ;;
  ci)
    [[ "$image" =~ ^ghcr\.io/[a-z0-9._/-]+(@sha256:[0-9a-f]{64}|:(sha-[0-9a-f]{40}|[0-9]+\.[0-9]+\.[0-9]+))$ ]] || {
      echo "DEPLOY_SOURCE=ci requires an immutable MetaServer image." >&2
      exit 1
    }
    ;;
  source) ;;
  *) echo "DEPLOY_SOURCE must be auto, ci, or source." >&2; exit 1 ;;
esac

[[ -f "$env_file" ]] || { echo "Missing $env_file" >&2; exit 1; }
if grep -Eq '^[A-Z0-9_]+=CHANGE_ME$|example\.com' "$env_file"; then
  echo "Refusing deployment: environment still contains CHANGE_ME or example.com." >&2
  exit 1
fi
chmod 600 "$env_file"

if docker info >/dev/null 2>&1; then
  docker_cmd=(docker)
elif sudo -n docker info >/dev/null 2>&1; then
  docker_cmd=(sudo docker)
else
  echo "Docker is unavailable." >&2
  exit 1
fi

compose=("${docker_cmd[@]}" compose --env-file "$env_file" -f "$compose_file")
if [[ -n "$compose_override_file" && "$compose_override_file" != /dev/null ]]; then
  [[ -f "$compose_override_file" ]] || { echo "Missing $compose_override_file" >&2; exit 1; }
  compose+=(-f "$compose_override_file")
fi
compose+=(--profile meta)
if [[ "$deploy_source" == "ci" ]]; then
  export META_SERVER_IMAGE="$image"
  "${compose[@]}" pull meta-server
else
  "${compose[@]}" build --pull meta-server
fi

# Provisioning runs after control-plane migration 28 and is idempotent. These
# one-shot containers never become long-running dependencies.
"${compose[@]}" run --rm meta-postgres-provision
"${compose[@]}" run --rm meta-redis-provision

# Updating MetaServer must not recreate or restart control-plane.
"${compose[@]}" up -d --no-deps meta-server

meta_port="$(sed -n 's/^META_SERVER_HTTP_PORT=//p' "$env_file" | tail -n 1)"
ready_url="http://127.0.0.1:${meta_port:-18082}/health/ready"
for _ in {1..60}; do
  if curl -fsS "$ready_url" | grep -q '"status":"ready"'; then
    "${compose[@]}" ps meta-server
    printf 'META_SERVER_DEPLOY_OK %s\n' "$ready_url"
    exit 0
  fi
  sleep 2
done
"${compose[@]}" logs --tail=200 meta-server >&2
echo "MetaServer did not become ready within 120 seconds." >&2
exit 1
