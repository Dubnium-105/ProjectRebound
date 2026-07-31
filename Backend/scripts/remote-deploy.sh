#!/usr/bin/env bash
set -euo pipefail

# Runs on the target host through deploy-over-ssh.sh stdin.
target="${1:?target is required}"
deploy_root="${2:?deploy root is required}"
release_id="${3:?release id is required}"
bundle="${4:?bundle is required}"
image="${5:?image is required}"
control_env_file="${6:?control env file is required}"
edge_env_file="${7:?edge env file is required}"
edge_config_file="${8:?edge config file is required}"
public_base_url="${9:?public base URL is required}"
enable_monitoring="${10:?monitoring setting is required}"
control_compose_override_file="${11:-/dev/null}"

[[ "$target" =~ ^(control-plane|meta-server|edge-relay)$ ]] || { echo "Invalid target" >&2; exit 1; }
[[ "$deploy_root" =~ ^/[A-Za-z0-9._/-]+$ ]] || { echo "Invalid deploy root" >&2; exit 1; }
[[ "$release_id" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "Invalid release id" >&2; exit 1; }
[[ "$bundle" == "/tmp/projectrebound-${release_id}.tar.gz" ]] || { echo "Unexpected bundle path" >&2; exit 1; }
[[ "$image" =~ ^ghcr\.io/[a-z0-9._/-]+:sha-[0-9a-f]{40}$ ]] || { echo "Invalid image" >&2; exit 1; }
for path in "$control_env_file" "$edge_env_file" "$edge_config_file" "$control_compose_override_file"; do
  [[ "$path" =~ ^/[A-Za-z0-9._/-]+$ ]] || { echo "Invalid deployment file path" >&2; exit 1; }
done
[[ "$public_base_url" =~ ^https?://[A-Za-z0-9._:/-]+$ ]] || { echo "Invalid public URL" >&2; exit 1; }
[[ "$enable_monitoring" =~ ^[01]$ ]] || { echo "Invalid monitoring setting" >&2; exit 1; }
[[ -f "$bundle" ]] || { echo "Deployment bundle is missing" >&2; exit 1; }
trap 'rm -f "$bundle"' EXIT HUP INT TERM

release_dir="$deploy_root/releases/$release_id"
current_link="$deploy_root/current-$target"
mkdir -p "$deploy_root/releases" "$deploy_root/backups"
[[ ! -e "$release_dir" ]] || { echo "Release already exists: $release_dir" >&2; exit 1; }
mkdir "$release_dir"
tar -xzf "$bundle" -C "$release_dir"
  chmod +x "$release_dir"/Backend/scripts/*.sh

previous_dir=""
if [[ -L "$current_link" ]]; then
  candidate="$(readlink -f "$current_link" || true)"
  if [[ "$candidate" == "$deploy_root"/releases/* && -d "$candidate" ]]; then previous_dir="$candidate"; fi
fi

deploy_release() {
  local directory="$1"
  local selected_image="$2"
  if [[ "$target" == "control-plane" ]]; then
    if ! CONTROL_PLANE_ENV_FILE="$control_env_file" \
      CONTROL_PLANE_IMAGE="$selected_image" \
      CONTROL_PLANE_COMPOSE_OVERRIDE_FILE="$control_compose_override_file" \
      DEPLOY_SOURCE=ci \
      ENABLE_MONITORING="$enable_monitoring" \
        "$directory/Backend/scripts/deploy-control-plane.sh"; then
      return 1
    fi
    if ! CONTROL_PLANE_ENV_FILE="$control_env_file" \
      PUBLIC_BASE_URL="$public_base_url" \
        "$directory/Backend/scripts/verify-control-plane.sh"; then
      return 1
    fi
  elif [[ "$target" == "meta-server" ]]; then
    if ! CONTROL_PLANE_ENV_FILE="$control_env_file" \
      META_SERVER_IMAGE="$selected_image" \
      CONTROL_PLANE_COMPOSE_OVERRIDE_FILE="$control_compose_override_file" \
      DEPLOY_SOURCE=ci \
        "$directory/Backend/scripts/deploy-meta-server.sh"; then
      return 1
    fi
  else
    cp "$edge_config_file" "$directory/Backend/deployments/edge-relay/config.edge-relay.yaml"
    # The container runs as UID 65532 and must be able to read this non-secret file.
    chmod 644 "$directory/Backend/deployments/edge-relay/config.edge-relay.yaml"
    if ! EDGE_RELAY_ENV_FILE="$edge_env_file" \
      EDGE_RELAY_IMAGE="$selected_image" \
      DEPLOY_SOURCE=ci \
        "$directory/Backend/scripts/deploy-edge-relay.sh"; then
      return 1
    fi
  fi
}

if [[ "$target" == "control-plane" ]]; then
  [[ -f "$control_env_file" ]] || { echo "Control-plane env file is missing" >&2; exit 1; }
  [[ "$control_compose_override_file" == /dev/null || -f "$control_compose_override_file" ]] || {
    echo "Control-plane Compose override file is missing" >&2
    exit 1
  }
  if docker info >/dev/null 2>&1; then docker_cmd=(docker); else docker_cmd=(sudo docker); fi
  if "${docker_cmd[@]}" ps --format '{{.Names}}' | grep -qx 'project-rebound-control-plane-postgres-1'; then
    CONTROL_PLANE_ENV_FILE="$control_env_file" \
      "$release_dir/Backend/scripts/backup-control-plane.sh" "$deploy_root/backups"
  fi
elif [[ "$target" == "meta-server" ]]; then
  [[ -f "$control_env_file" ]] || { echo "Control-plane env file is missing for MetaServer" >&2; exit 1; }
  [[ "$control_compose_override_file" == /dev/null || -f "$control_compose_override_file" ]] || {
    echo "Control-plane Compose override file is missing for MetaServer" >&2
    exit 1
  }
else
  [[ -f "$edge_env_file" ]] || { echo "Edge env file is missing" >&2; exit 1; }
  [[ -f "$edge_config_file" ]] || { echo "Edge config file is missing" >&2; exit 1; }
fi

if ! deploy_release "$release_dir" "$image"; then
  echo "Deployment failed; attempting previous release rollback." >&2
  if [[ -n "$previous_dir" && -f "$previous_dir/.deployed-image" ]]; then
    previous_image="$(cat "$previous_dir/.deployed-image")"
    if [[ "$previous_image" =~ ^ghcr\.io/[a-z0-9._/-]+:sha-[0-9a-f]{40}$ ]] &&
       deploy_release "$previous_dir" "$previous_image"; then
      echo "ROLLBACK_OK target=$target release=$previous_dir" >&2
    else
      echo "ROLLBACK_FAILED target=$target" >&2
    fi
  else
    echo "No previous release is available for rollback." >&2
  fi
  exit 1
fi

printf '%s\n' "$image" >"$release_dir/.deployed-image"
ln -sfn "$release_dir" "$current_link"
printf 'REMOTE_DEPLOY_OK target=%s release=%s image=%s\n' "$target" "$release_id" "$image"
