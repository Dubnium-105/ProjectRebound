#!/usr/bin/env bash
set -euo pipefail

target="${1:?usage: rollback.sh control-plane|edge-relay PREVIOUS_IMAGE}"
previous_image="${2:?previous image is required}"
script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
scripts_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"

[[ "$target" =~ ^(control-plane|edge-relay)$ ]] || { echo 'Invalid rollback target.' >&2; exit 1; }
[[ "$previous_image" != *:latest ]] || { echo 'Floating latest image is forbidden.' >&2; exit 1; }
[[ "$previous_image" =~ ^[A-Za-z0-9._/-]+(@sha256:[0-9a-f]{64}|:(sha-[0-9a-f]{40}|[0-9]+\.[0-9]+\.[0-9]+))$ ]] || {
  echo 'Rollback image must use a digest, immutable commit tag, or semantic version.' >&2; exit 1;
}

if [[ "$target" == control-plane ]]; then
  CONTROL_PLANE_IMAGE="$previous_image" DEPLOY_SOURCE=ci "$scripts_dir/deploy-control-plane.sh"
  "$scripts_dir/verify-control-plane.sh"
else
  EDGE_RELAY_IMAGE="$previous_image" DEPLOY_SOURCE=ci "$scripts_dir/deploy-edge-relay.sh"
fi
printf 'ROLLBACK_COMPLETE target=%s image=%s database_rollback=false\n' "$target" "$previous_image"
