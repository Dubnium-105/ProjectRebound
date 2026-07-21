#!/usr/bin/env sh
set -eu

if [ "${CHAOS_I_UNDERSTAND:-}" != "disposable-staging" ] || [ "${PROJECTREBOUND_ENVIRONMENT:-test}" = "production" ]; then
  echo "requires CHAOS_I_UNDERSTAND=disposable-staging and a non-production environment" >&2; exit 1
fi
project="${CHAOS_PROJECT:-project-rebound-chaos}"
case "$project" in project-rebound-chaos*) ;; *) echo "CHAOS_PROJECT must start with project-rebound-chaos" >&2; exit 1;; esac
compose_file="${CHAOS_COMPOSE_FILE:-deployments/control-plane/docker-compose.yaml}"
action="${1:-}"; service="${2:-}"; hold="${CHAOS_HOLD_SECONDS:-10}"
case "$service" in control-plane|postgres|redis|edge-relay) ;; *) echo "unsupported service" >&2; exit 2;; esac

compose() { docker compose -p "$project" -f "$compose_file" "$@"; }
case "$action" in
  restart) compose restart "$service" ;;
  sigkill) compose kill -s SIGKILL "$service"; compose up -d "$service" ;;
  pause)
    compose pause "$service"
    trap 'compose unpause "$service" >/dev/null 2>&1 || true' EXIT INT TERM
    sleep "$hold"
    compose unpause "$service"
    trap - EXIT INT TERM
    ;;
  *) echo "usage: $0 restart|sigkill|pause control-plane|postgres|redis|edge-relay" >&2; exit 2 ;;
esac
