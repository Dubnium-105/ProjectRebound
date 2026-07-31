#!/usr/bin/env bash
set -euo pipefail

# Runs on a GitHub-hosted runner. SSH credentials and known_hosts must already
# be installed by the workflow. All deployment settings arrive through a
# protected GitHub Environment.
: "${DEPLOY_TARGET:?DEPLOY_TARGET is required}"
: "${DEPLOY_HOST:?DEPLOY_HOST is required}"
: "${DEPLOY_USER:?DEPLOY_USER is required}"
: "${DEPLOY_PORT:?DEPLOY_PORT is required}"
: "${DEPLOY_ROOT:?DEPLOY_ROOT is required}"
: "${DEPLOY_RELEASE_ID:?DEPLOY_RELEASE_ID is required}"
: "${DEPLOY_IMAGE:?DEPLOY_IMAGE is required}"
: "${GHCR_USERNAME:?GHCR_USERNAME is required}"
: "${GHCR_TOKEN:?GHCR_TOKEN is required}"

[[ "$DEPLOY_TARGET" =~ ^(control-plane|meta-server|edge-relay)$ ]] || { echo "Invalid DEPLOY_TARGET" >&2; exit 1; }
[[ "$DEPLOY_HOST" =~ ^[A-Za-z0-9.-]+$ ]] || { echo "Invalid DEPLOY_HOST" >&2; exit 1; }
[[ "$DEPLOY_USER" =~ ^[a-z_][a-z0-9_-]*$ ]] || { echo "Invalid DEPLOY_USER" >&2; exit 1; }
[[ "$DEPLOY_PORT" =~ ^[0-9]{1,5}$ ]] || { echo "Invalid DEPLOY_PORT" >&2; exit 1; }
[[ "$DEPLOY_ROOT" =~ ^/[A-Za-z0-9._/-]+$ ]] || { echo "Invalid DEPLOY_ROOT" >&2; exit 1; }
[[ "$DEPLOY_RELEASE_ID" =~ ^[A-Za-z0-9._-]+$ ]] || { echo "Invalid DEPLOY_RELEASE_ID" >&2; exit 1; }
[[ "$DEPLOY_IMAGE" =~ ^ghcr\.io/[a-z0-9._/-]+:sha-[0-9a-f]{40}$ ]] || { echo "Invalid DEPLOY_IMAGE" >&2; exit 1; }
[[ "$GHCR_USERNAME" =~ ^[A-Za-z0-9_.-]+$ ]] || { echo "Invalid GHCR_USERNAME" >&2; exit 1; }

control_env_file="${CONTROL_PLANE_ENV_FILE:-/dev/null}"
edge_env_file="${EDGE_RELAY_ENV_FILE:-/dev/null}"
edge_config_file="${EDGE_RELAY_CONFIG_FILE:-/dev/null}"
control_compose_override_file="${CONTROL_PLANE_COMPOSE_OVERRIDE_FILE:-/dev/null}"
public_base_url="${PUBLIC_BASE_URL:-http://127.0.0.1:8080}"
enable_monitoring="${ENABLE_MONITORING:-1}"
for path in "$control_env_file" "$edge_env_file" "$edge_config_file" "$control_compose_override_file"; do
  [[ "$path" =~ ^/[A-Za-z0-9._/-]+$ ]] || { echo "Invalid deployment file path" >&2; exit 1; }
done
[[ "$public_base_url" =~ ^https?://[A-Za-z0-9._:/-]+$ ]] || { echo "Invalid PUBLIC_BASE_URL" >&2; exit 1; }
[[ "$enable_monitoring" =~ ^[01]$ ]] || { echo "Invalid ENABLE_MONITORING" >&2; exit 1; }

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
backend_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"
repository_dir="$(CDPATH= cd -- "$backend_dir/.." && pwd)"
temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM
bundle="$temporary_dir/projectrebound-deployment.tar.gz"

tar -C "$repository_dir" \
  --exclude='Backend/deployments/control-plane/.env' \
  --exclude='Backend/deployments/edge-relay/.env' \
  --exclude='Backend/deployments/edge-relay/config.edge-relay.yaml' \
  --exclude='Backend/backups' \
  -czf "$bundle" Backend/deployments Backend/scripts AdminWeb

ssh_target="${DEPLOY_USER}@${DEPLOY_HOST}"
ssh_options=(-p "$DEPLOY_PORT" -o BatchMode=yes -o StrictHostKeyChecking=yes)
remote_bundle="/tmp/projectrebound-${DEPLOY_RELEASE_ID}.tar.gz"

# The token is sent only through encrypted stdin and is never part of a remote
# command argument or deployment bundle.
printf '%s' "$GHCR_TOKEN" | ssh "${ssh_options[@]}" "$ssh_target" \
  "if docker info >/dev/null 2>&1; then docker login ghcr.io --username '$GHCR_USERNAME' --password-stdin; else sudo docker login ghcr.io --username '$GHCR_USERNAME' --password-stdin; fi" >/dev/null

scp -P "$DEPLOY_PORT" -o BatchMode=yes -o StrictHostKeyChecking=yes \
  "$bundle" "$ssh_target:$remote_bundle"

ssh "${ssh_options[@]}" "$ssh_target" \
  "bash -s -- '$DEPLOY_TARGET' '$DEPLOY_ROOT' '$DEPLOY_RELEASE_ID' '$remote_bundle' '$DEPLOY_IMAGE' '$control_env_file' '$edge_env_file' '$edge_config_file' '$public_base_url' '$enable_monitoring' '$control_compose_override_file'" \
  <"$script_dir/remote-deploy.sh"
