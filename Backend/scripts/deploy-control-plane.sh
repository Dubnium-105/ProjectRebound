#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
backend_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"
deployment_dir="$backend_dir/deployments/control-plane"
env_file="${CONTROL_PLANE_ENV_FILE:-$deployment_dir/.env}"
compose_file="$deployment_dir/docker-compose.yaml"
image="${CONTROL_PLANE_IMAGE:-}"
admin_web_image="${ADMIN_WEB_IMAGE:-}"

deploy_source="${DEPLOY_SOURCE:-auto}"
if [[ -z "${DEPLOY_SOURCE:-}" && "${DEPLOY_PULL_ONLY:-0}" == "1" ]]; then
  deploy_source="ci"
fi
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
      echo "DEPLOY_SOURCE=ci requires an immutable GHCR digest, commit tag, or semantic version." >&2
      exit 1
    }
    ;;
  source) ;;
  *)
    echo "DEPLOY_SOURCE must be auto, ci, or source." >&2
    exit 1
    ;;
esac

if [[ "$deploy_source" == "ci" && -z "$admin_web_image" ]]; then
  admin_web_image="${image/projectrebound-control-plane/projectrebound-admin-web}"
fi
if [[ "$deploy_source" == "ci" &&
      ! "$admin_web_image" =~ ^ghcr\.io/[a-z0-9._/-]+(@sha256:[0-9a-f]{64}|:(sha-[0-9a-f]{40}|[0-9]+\.[0-9]+\.[0-9]+))$ ]]; then
  echo "DEPLOY_SOURCE=ci requires ADMIN_WEB_IMAGE to be an immutable GHCR digest, commit tag, or semantic version." >&2
  exit 1
fi

if [[ ! -f "$env_file" ]]; then
  echo "Missing $env_file. Run scripts/generate-control-plane-env.sh first." >&2
  exit 1
fi
if grep -Eq '^[A-Z0-9_]+=CHANGE_ME$|example\.com' "$env_file"; then
  echo "Refusing deployment: $env_file still contains CHANGE_ME or example.com values." >&2
  exit 1
fi
chmod 600 "$env_file"

if docker info >/dev/null 2>&1; then
  docker_cmd=(docker)
elif sudo -n docker info >/dev/null 2>&1; then
  docker_cmd=(sudo docker)
else
  echo "Docker is unavailable to the current user and passwordless sudo docker failed." >&2
  exit 1
fi

compose=("${docker_cmd[@]}" compose --env-file "$env_file" -f "$compose_file")
if [[ -n "$admin_web_image" ]]; then
  export ADMIN_WEB_IMAGE="$admin_web_image"
fi
if [[ "${ENABLE_MONITORING:-1}" == "1" ]]; then compose+=(--profile monitoring); fi
"${compose[@]}" config -q
if [[ "$deploy_source" == "ci" ]]; then
  "${compose[@]}" pull
else
  "${compose[@]}" build --pull control-plane admin-web
fi
printf 'CONTROL_PLANE_DEPLOY_SOURCE source=%s image=%s admin_web_image=%s\n' \
  "$deploy_source" "${image:-local-build}" "${admin_web_image:-local-build}"
"${compose[@]}" up -d --remove-orphans

admin_port="$(sed -n 's/^CONTROL_PLANE_ADMIN_PORT=//p' "$env_file" | tail -n 1)"
admin_port="${admin_port:-18080}"
ready_url="http://127.0.0.1:${admin_port}/health/ready"
for _ in {1..60}; do
  if curl -fsS "$ready_url" | grep -q '"status":"ready"'; then
    "${compose[@]}" ps
    printf 'CONTROL_PLANE_DEPLOY_OK %s\n' "$ready_url"
    exit 0
  fi
  sleep 2
done
"${compose[@]}" ps >&2
"${compose[@]}" logs --tail=200 control-plane >&2
echo "Control plane did not become ready within 120 seconds." >&2
exit 1
