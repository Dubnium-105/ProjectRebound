#!/usr/bin/env sh
set -eu

if [ "${NETEM_I_UNDERSTAND:-}" != "disposable-container" ]; then
  echo "Refusing to run: set NETEM_I_UNDERSTAND=disposable-container." >&2
  exit 2
fi
container="${1:-}"
profile="${2:-}"
command -v docker >/dev/null || { echo "docker is required" >&2; exit 2; }
command -v nsenter >/dev/null || { echo "nsenter is required" >&2; exit 2; }
command -v tc >/dev/null || { echo "tc from iproute2 is required" >&2; exit 2; }
if [ "$(id -u)" -eq 0 ]; then
  run_netns() { nsenter "$@"; }
else
  sudo -n true >/dev/null 2>&1 || { echo "root or passwordless sudo is required" >&2; exit 2; }
  run_netns() { sudo -n nsenter "$@"; }
fi
case "$container" in
  ''|*[!a-zA-Z0-9_.-]*) echo "invalid container identifier" >&2; exit 2 ;;
esac
project="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.project" }}' "$container")"
service="$(docker inspect --format '{{ index .Config.Labels "com.docker.compose.service" }}' "$container")"
pid="$(docker inspect --format '{{ .State.Pid }}' "$container")"
case "$pid" in
  ''|*[!0-9]*|0) echo "Relay container is not running" >&2; exit 2 ;;
esac
case "$project" in
  project-rebound-v11-*) ;;
  *) echo "container is not owned by a disposable V1.1 integration project" >&2; exit 2 ;;
esac
case "$service" in
  edge-relay-a|edge-relay-b) ;;
  *) echo "container is not an integration Edge Relay" >&2; exit 2 ;;
esac

case "$profile" in
  mild)
    run_netns -t "$pid" -n -- tc qdisc replace dev eth0 root netem delay 50ms 10ms distribution normal loss 1%
    ;;
  moderate)
    run_netns -t "$pid" -n -- tc qdisc replace dev eth0 root netem delay 120ms 30ms distribution normal loss 5% rate 2mbit
    ;;
  severe)
    run_netns -t "$pid" -n -- tc qdisc replace dev eth0 root netem delay 250ms 80ms distribution normal loss 15% reorder 3% rate 256kbit
    ;;
  reset)
    run_netns -t "$pid" -n -- tc qdisc del dev eth0 root 2>/dev/null || true
    ;;
  *) echo "usage: $0 CONTAINER mild|moderate|severe|reset" >&2; exit 2 ;;
esac
run_netns -t "$pid" -n -- tc qdisc show dev eth0
