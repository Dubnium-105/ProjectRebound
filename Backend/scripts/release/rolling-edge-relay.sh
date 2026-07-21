#!/usr/bin/env bash
set -euo pipefail

: "${RELAY_NODE_ID:?RELAY_NODE_ID is required}"
: "${RELAY_ADMIN_BASE_URL:?RELAY_ADMIN_BASE_URL is required}"
: "${RELAY_ADMIN_TOKEN:?RELAY_ADMIN_TOKEN is required}"
: "${EDGE_RELAY_IMAGE:?EDGE_RELAY_IMAGE is required}"
script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
scripts_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"
deadline_seconds="${RELAY_DRAIN_DEADLINE_SECONDS:-600}"
poll_seconds="${RELAY_DRAIN_POLL_SECONDS:-5}"

command -v jq >/dev/null || { echo 'jq is required.' >&2; exit 1; }
[[ "$deadline_seconds" =~ ^[0-9]+$ && "$deadline_seconds" -ge 30 && "$deadline_seconds" -le 86400 ]] || {
  echo 'Invalid drain deadline.' >&2; exit 1;
}
header_file="$(mktemp)"
chmod 600 "$header_file"
printf 'Authorization: Bearer %s\nContent-Type: application/json\n' "$RELAY_ADMIN_TOKEN" >"$header_file"
unset RELAY_ADMIN_TOKEN
api() { curl -fsS --max-time 15 -H "@$header_file" "$@"; }

previous_image="${PREVIOUS_EDGE_RELAY_IMAGE:-}"
if [[ -z "$previous_image" ]]; then
  if docker info >/dev/null 2>&1; then docker_cmd=(docker); else docker_cmd=(sudo docker); fi
  container_name="${EDGE_RELAY_CONTAINER_NAME:-project-rebound-edge-relay}"
  previous_image="$("${docker_cmd[@]}" inspect -f '{{.Config.Image}}' "$container_name")"
fi

rollback_needed=1
on_exit() {
  status=$?
  if [[ $status -ne 0 && $rollback_needed -eq 1 && -n "$previous_image" ]]; then
    "$script_dir/rollback.sh" edge-relay "$previous_image" || true
    api -X POST "$RELAY_ADMIN_BASE_URL/internal/v1/relay-nodes/$RELAY_NODE_ID/resume" >/dev/null || true
  fi
  rm -f -- "$header_file"
}
trap on_exit EXIT

api -X POST -d "{\"deadline_seconds\":$deadline_seconds,\"migrate_existing\":true}" \
  "$RELAY_ADMIN_BASE_URL/internal/v1/relay-nodes/$RELAY_NODE_ID/drain" >/dev/null
deadline=$(( $(date -u +%s) + deadline_seconds ))
while :; do
  node="$(api "$RELAY_ADMIN_BASE_URL/internal/v1/relay-nodes/$RELAY_NODE_ID")"
  allocations="$(printf '%s' "$node" | jq -er '.data.active_allocations')"
  [[ "$allocations" == 0 ]] && break
  [[ "$(date -u +%s)" -lt "$deadline" ]] || { echo 'Relay drain deadline expired.' >&2; exit 1; }
  sleep "$poll_seconds"
done

DEPLOY_SOURCE=ci "$scripts_dir/deploy-edge-relay.sh"
api -X POST "$RELAY_ADMIN_BASE_URL/internal/v1/relay-nodes/$RELAY_NODE_ID/resume" >/dev/null
node="$(api "$RELAY_ADMIN_BASE_URL/internal/v1/relay-nodes/$RELAY_NODE_ID")"
[[ "$(printf '%s' "$node" | jq -er '.data.state')" == READY ]] || { echo 'Relay did not return to READY.' >&2; exit 1; }
rollback_needed=0
printf 'EDGE_RELAY_ROLLING_UPGRADE_OK node_id=%s image=%s\n' "$RELAY_NODE_ID" "$EDGE_RELAY_IMAGE"
