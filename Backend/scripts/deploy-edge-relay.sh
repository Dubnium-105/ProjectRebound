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

[[ "$(uname -s)" == "Linux" ]] || { echo "The separated edge stack requires Linux host networking." >&2; exit 1; }
[[ -f "$config_file" ]] || { echo "Missing $config_file; copy and edit config.edge-relay.yaml.example." >&2; exit 1; }
[[ -f "$env_file" ]] || { echo "Missing $env_file; copy .env.example and set the first-enrollment token." >&2; exit 1; }
if grep -Eq 'CHANGE_ME|example\.com|203\.0\.113\.20|10\.20\.0\.10' "$config_file" "$env_file"; then
  echo "Refusing deployment: edge configuration still contains placeholder values." >&2
  exit 1
fi
chmod 600 "$env_file"
chmod 644 "$config_file"

if docker info >/dev/null 2>&1; then
  docker_cmd=(docker)
elif sudo -n docker info >/dev/null 2>&1; then
  docker_cmd=(sudo docker)
else
  echo "Docker is unavailable to the current user and passwordless sudo docker failed." >&2
  exit 1
fi

container_name="${EDGE_RELAY_CONTAINER_NAME:-project-rebound-edge-relay}"
volume_name="${EDGE_RELAY_VOLUME_NAME:-project-rebound-edge-relay-data}"
[[ "$container_name" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]+$ ]] || { echo "Invalid EDGE_RELAY_CONTAINER_NAME." >&2; exit 1; }
[[ "$volume_name" =~ ^[A-Za-z0-9][A-Za-z0-9_.-]+$ ]] || { echo "Invalid EDGE_RELAY_VOLUME_NAME." >&2; exit 1; }

runtime_preference="${EDGE_RELAY_RUNTIME:-auto}"
[[ "$runtime_preference" =~ ^(auto|compose|raw-docker)$ ]] || {
  echo "EDGE_RELAY_RUNTIME must be auto, compose, or raw-docker." >&2
  exit 1
}
runtime_mode=raw-docker
if [[ "$runtime_preference" != "raw-docker" ]] && "${docker_cmd[@]}" compose version >/dev/null 2>&1; then
  runtime_mode=compose
  compose=("${docker_cmd[@]}" compose --env-file "$env_file" -f "$compose_file")
elif [[ "$runtime_preference" != "raw-docker" ]] && command -v docker-compose >/dev/null 2>&1; then
  runtime_mode=compose
  if [[ "${docker_cmd[0]}" == "sudo" ]]; then
    compose=(sudo docker-compose --env-file "$env_file" -f "$compose_file")
  else
    compose=(docker-compose --env-file "$env_file" -f "$compose_file")
  fi
fi
if [[ "$runtime_preference" == "compose" && "$runtime_mode" != "compose" ]]; then
  echo "EDGE_RELAY_RUNTIME=compose requested, but Docker Compose is unavailable." >&2
  exit 1
fi

raw_image="${image:-project-rebound/edge-relay:local}"

runtime_prepare() {
  if [[ "$runtime_mode" == "compose" ]]; then
    "${compose[@]}" config -q
    if [[ "$deploy_source" == "ci" ]]; then
      "${compose[@]}" pull edge-relay
    else
      "${compose[@]}" build --pull edge-relay
    fi
    return
  fi

  if [[ "$deploy_source" == "ci" ]]; then
    "${docker_cmd[@]}" pull "$raw_image"
  else
    "${docker_cmd[@]}" build --pull -f "$backend_dir/deployments/relay/Dockerfile" -t "$raw_image" "$backend_dir"
  fi
  "${docker_cmd[@]}" volume create "$volume_name" >/dev/null
}

runtime_up() {
  if [[ "$runtime_mode" == "compose" ]]; then
    "${compose[@]}" up -d "$@" edge-relay
    return
  fi

  if "${docker_cmd[@]}" ps -aq -f "name=^/${container_name}$" | grep -q .; then
    "${docker_cmd[@]}" rm -f "$container_name" >/dev/null
  fi
  "${docker_cmd[@]}" run -d \
    --name "$container_name" \
    --restart unless-stopped \
    --network host \
    --read-only \
    --security-opt no-new-privileges:true \
    --cap-drop ALL \
    --env-file "$env_file" \
    --mount "type=bind,src=$config_file,dst=/etc/projectrebound/config.edge-relay.yaml,readonly" \
    --mount "type=volume,src=$volume_name,dst=/edge-relay-data" \
    "$raw_image" >/dev/null
}

runtime_running() {
  if [[ "$runtime_mode" == "compose" ]]; then
    "${compose[@]}" ps --status running -q edge-relay | grep -q .
  else
    [[ "$("${docker_cmd[@]}" inspect -f '{{.State.Running}}' "$container_name" 2>/dev/null || true)" == "true" ]]
  fi
}

runtime_logs() {
  if [[ "$runtime_mode" == "compose" ]]; then
    "${compose[@]}" logs --no-color --tail="${1:-100}" edge-relay
  else
    "${docker_cmd[@]}" logs --tail="${1:-100}" "$container_name"
  fi
}

runtime_ps() {
  if [[ "$runtime_mode" == "compose" ]]; then
    "${compose[@]}" ps
  else
    "${docker_cmd[@]}" ps -a --filter "name=^/${container_name}$"
  fi
}

runtime_prepare
printf 'EDGE_RELAY_DEPLOY_SOURCE source=%s image=%s\n' "$deploy_source" "${image:-local-build}"
printf 'EDGE_RELAY_RUNTIME mode=%s\n' "$runtime_mode"
if [[ "$runtime_mode" == "compose" ]]; then
  runtime_up --remove-orphans
else
  runtime_up
fi

wait_connected() {
  for _ in {1..60}; do
    if runtime_running && runtime_logs 100 2>&1 | grep -q 'relay control connected'; then
      return 0
    fi
    sleep 2
  done
  return 1
}

if ! wait_connected; then
  runtime_ps >&2
  runtime_logs 200 >&2
  echo "Edge relay did not connect within 120 seconds." >&2
  exit 1
fi

# Remove the one-time secret and recreate once to prove identity persistence.
if grep -Eq '^EDGE_RELAY_BOOTSTRAP_TOKEN=.{32,}$' "$env_file"; then
  temporary_env="${env_file}.tmp"
  sed 's/^EDGE_RELAY_BOOTSTRAP_TOKEN=.*/EDGE_RELAY_BOOTSTRAP_TOKEN=/' "$env_file" >"$temporary_env"
  chmod 600 "$temporary_env"
  mv "$temporary_env" "$env_file"
  if [[ "$runtime_mode" == "compose" ]]; then
    runtime_up --force-recreate
  else
    runtime_up
  fi
  if ! wait_connected; then
    runtime_logs 200 >&2
    echo "Edge identity persistence verification failed after token removal." >&2
    exit 1
  fi
fi

runtime_ps
runtime_logs 20
printf 'EDGE_RELAY_DEPLOY_OK bootstrap_token_removed=true\n'
