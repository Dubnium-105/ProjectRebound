#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
backend_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"
deployment_dir="$backend_dir/deployments/edge-relay"
env_file="${EDGE_RELAY_ENV_FILE:-$deployment_dir/.env}"
compose_file="$deployment_dir/docker-compose.yaml"
config_file="$deployment_dir/config.edge-relay.yaml"
image="${EDGE_RELAY_IMAGE:-}"

deploy_source="${DEPLOY_SOURCE:-auto}"
if [[ -z "${DEPLOY_SOURCE:-}" && "${DEPLOY_PULL_ONLY:-0}" == "1" ]]; then
  deploy_source="ci"
fi
case "$deploy_source" in
  auto)
    if [[ "$image" =~ ^ghcr\.io/[a-z0-9._/-]+:sha-[0-9a-f]{40}$ ]]; then
      deploy_source="ci"
    else
      deploy_source="source"
    fi
    ;;
  ci)
    [[ "$image" =~ ^ghcr\.io/[a-z0-9._/-]+:sha-[0-9a-f]{40}$ ]] || {
      echo "DEPLOY_SOURCE=ci requires EDGE_RELAY_IMAGE=ghcr.io/...:sha-<40-char-commit>." >&2
      exit 1
    }
    ;;
  source) ;;
  *)
    echo "DEPLOY_SOURCE must be auto, ci, or source." >&2
    exit 1
    ;;
esac

[[ "$(uname -s)" == "Linux" ]] || { echo "The separated edge stack requires Linux host networking." >&2; exit 1; }
[[ -f "$config_file" ]] || { echo "Missing $config_file; copy and edit config.edge-relay.yaml.example." >&2; exit 1; }
[[ -f "$env_file" ]] || { echo "Missing $env_file; copy .env.example and set the first-enrollment token." >&2; exit 1; }
if grep -Eq 'CHANGE_ME|example\.com|203\.0\.113\.20|10\.20\.0\.10' "$config_file" "$env_file"; then
  echo "Refusing deployment: edge configuration still contains placeholder values." >&2
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
"${compose[@]}" config -q
if [[ "$deploy_source" == "ci" ]]; then
  "${compose[@]}" pull edge-relay
else
  "${compose[@]}" build --pull edge-relay
fi
printf 'EDGE_RELAY_DEPLOY_SOURCE source=%s image=%s\n' "$deploy_source" "${image:-local-build}"
"${compose[@]}" up -d --remove-orphans

wait_connected() {
  for _ in {1..60}; do
    if "${compose[@]}" ps --status running -q edge-relay | grep -q . &&
       "${compose[@]}" logs --no-color --tail=100 edge-relay 2>&1 | grep -q 'relay control connected'; then
      return 0
    fi
    sleep 2
  done
  return 1
}

if ! wait_connected; then
  "${compose[@]}" ps >&2
  "${compose[@]}" logs --no-color --tail=200 edge-relay >&2
  echo "Edge relay did not connect within 120 seconds." >&2
  exit 1
fi

# Remove the one-time secret and recreate once to prove identity persistence.
if grep -Eq '^EDGE_RELAY_BOOTSTRAP_TOKEN=.{32,}$' "$env_file"; then
  temporary_env="${env_file}.tmp"
  sed 's/^EDGE_RELAY_BOOTSTRAP_TOKEN=.*/EDGE_RELAY_BOOTSTRAP_TOKEN=/' "$env_file" >"$temporary_env"
  chmod 600 "$temporary_env"
  mv "$temporary_env" "$env_file"
  "${compose[@]}" up -d --force-recreate edge-relay
  if ! wait_connected; then
    "${compose[@]}" logs --no-color --tail=200 edge-relay >&2
    echo "Edge identity persistence verification failed after token removal." >&2
    exit 1
  fi
fi

"${compose[@]}" ps
"${compose[@]}" logs --no-color --tail=20 edge-relay
printf 'EDGE_RELAY_DEPLOY_OK bootstrap_token_removed=true\n'
