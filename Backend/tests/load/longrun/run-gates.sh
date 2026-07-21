#!/usr/bin/env bash
set -Eeuo pipefail

if [[ "${V11_LONGRUN_I_UNDERSTAND:-}" != "isolated-docker-stack" ]]; then
  echo "Refusing to run: set V11_LONGRUN_I_UNDERSTAND=isolated-docker-stack." >&2
  exit 64
fi

script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
load_dir="$(cd -- "$script_dir/.." && pwd)"
project="${V11_LONGRUN_PROJECT:-project-rebound-v11-longrun-$(date -u +%Y%m%d%H%M%S)}"
results_dir="${V11_LONGRUN_RESULTS_DIR:-${TMPDIR:-/tmp}/$project-results}"
artifacts_dir="$results_dir/artifacts"
secrets_file="$results_dir/secrets.env"
status_file="$results_dir/status.env"
events_file="$results_dir/events.tsv"
compose_file="$script_dir/docker-compose.yaml"

case "$project" in
  project-rebound-v11-longrun-*) ;;
  *) echo "Project name must start with project-rebound-v11-longrun-." >&2; exit 64 ;;
esac

for command_name in docker openssl curl python3; do
  command -v "$command_name" >/dev/null 2>&1 || {
    echo "$command_name is required" >&2
    exit 69
  }
done
docker compose version >/dev/null

: "${V11_CONTROL_PLANE_IMAGE:?V11_CONTROL_PLANE_IMAGE is required}"
: "${V11_EDGE_RELAY_IMAGE:?V11_EDGE_RELAY_IMAGE is required}"
: "${V11_LOAD_BOT_IMAGE:?V11_LOAD_BOT_IMAGE is required}"

umask 077
mkdir -p "$results_dir/scenarios" "$artifacts_dir"
chmod 755 "$results_dir" "$results_dir/scenarios"
chown 65532:65532 "$artifacts_dir"
chmod 700 "$artifacts_dir"

write_status() {
  local state="$1"
  local gate="${2:-none}"
  local temporary="$status_file.tmp"
  {
    printf 'state=%q\n' "$state"
    printf 'gate=%q\n' "$gate"
    printf 'project=%q\n' "$project"
    printf 'updated_at=%q\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    printf 'results_dir=%q\n' "$results_dir"
  } >"$temporary"
  mv "$temporary" "$status_file"
}

record_event() {
  printf '%s\t%s\t%s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$1" "${2:-}" >>"$events_file"
}

generate_secrets() {
  local temporary_dir
  temporary_dir="$(mktemp -d)"
  trap 'rm -rf "$temporary_dir"' RETURN
  openssl genpkey -algorithm ED25519 -out "$temporary_dir/relay-ca.key" >/dev/null 2>&1
  openssl req -new -x509 -key "$temporary_dir/relay-ca.key" \
    -out "$temporary_dir/relay-ca.crt" -days 3650 \
    -subj '/CN=Project Rebound V1.1 Longrun Relay CA' \
    -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
    -addext 'keyUsage=critical,keyCertSign,cRLSign,digitalSignature' >/dev/null 2>&1
  {
    printf 'V11_CONTROL_PLANE_IMAGE=%s\n' "$V11_CONTROL_PLANE_IMAGE"
    printf 'V11_EDGE_RELAY_IMAGE=%s\n' "$V11_EDGE_RELAY_IMAGE"
    printf 'V11_LOAD_BOT_IMAGE=%s\n' "$V11_LOAD_BOT_IMAGE"
    printf 'V11_CONTROL_HTTP_PORT=38080\n'
    printf 'V11_ADMIN_TOKEN=%s\n' "$(openssl rand -hex 48)"
    printf 'V11_RELAY_A_BOOTSTRAP_TOKEN=%s\n' "$(openssl rand -hex 48)"
    printf 'V11_RELAY_B_BOOTSTRAP_TOKEN=%s\n' "$(openssl rand -hex 48)"
    printf 'V11_ACCESS_TOKEN_PRIVATE_KEY_BASE64=%s\n' "$(openssl rand -base64 32 | tr -d '\r\n')"
    printf 'V11_RELAY_TOKEN_PRIVATE_KEY_BASE64=%s\n' "$(openssl rand -base64 32 | tr -d '\r\n')"
    printf 'V11_UPDATE_SIGNING_PRIVATE_KEY_BASE64=%s\n' "$(openssl rand -base64 32 | tr -d '\r\n')"
    printf 'V11_RELAY_CA_CERT_PEM_BASE64=%s\n' "$(openssl base64 -A -in "$temporary_dir/relay-ca.crt")"
    printf 'V11_RELAY_CA_KEY_PEM_BASE64=%s\n' "$(openssl base64 -A -in "$temporary_dir/relay-ca.key")"
  } >"$secrets_file"
  chmod 600 "$secrets_file"
  trap - RETURN
  rm -rf "$temporary_dir"
}

if [[ ! -f "$secrets_file" ]]; then
  generate_secrets
else
  for image_entry in \
    "V11_CONTROL_PLANE_IMAGE=$V11_CONTROL_PLANE_IMAGE" \
    "V11_EDGE_RELAY_IMAGE=$V11_EDGE_RELAY_IMAGE" \
    "V11_LOAD_BOT_IMAGE=$V11_LOAD_BOT_IMAGE"; do
    grep -Fqx "$image_entry" "$secrets_file" || {
      echo "Existing secrets file belongs to a different image set: $secrets_file" >&2
      exit 64
    }
  done
fi
compose=(docker compose --project-name "$project" --env-file "$secrets_file" --file "$compose_file")

prepare_scenario() {
  local source="$1"
  local destination="$2"
  sed \
    -e 's#https://staging-api.example.com#http://control-plane:8080#g' \
    -e 's#wss://staging-api.example.com/v1/realtime/connect#ws://control-plane:8080/v1/realtime/connect#g' \
    -e 's#CHANGE_ME_STAGING_INVITE##g' \
    "$source" >"$destination"
  chmod 644 "$destination"
}

prepare_scenario "$load_dir/scenario-v1.1-basic.yaml" "$results_dir/scenarios/basic-1h.yaml"
prepare_scenario "$load_dir/scenario-v1.1-full.yaml" "$results_dir/scenarios/full-6h.yaml"
prepare_scenario "$load_dir/scenario-v1.1-relay-soak.yaml" "$results_dir/scenarios/relay-soak-24h.yaml"
sed 's/^duration: 1h$/duration: 3m/' "$results_dir/scenarios/basic-1h.yaml" >"$results_dir/scenarios/preflight-3m.yaml"
chmod 644 "$results_dir/scenarios/preflight-3m.yaml"

wait_ready() {
  local deadline=$((SECONDS + 180))
  until curl --fail --silent --show-error http://127.0.0.1:38080/health/ready >/dev/null; do
    (( SECONDS < deadline )) || return 1
    sleep 2
  done
  until [[ "$(curl --fail --silent http://127.0.0.1:38080/internal/metrics | awk '/^relay_node_control_connected/ {sum += $2} END {print sum + 0}')" == "2" ]]; do
    (( SECONDS < deadline )) || return 1
    sleep 2
  done
}

reset_stack() {
  write_status RESETTING "$1"
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  "${compose[@]}" up --detach --wait
  wait_ready
  record_event stack_ready "$1"
}

collect_telemetry() {
  local gate="$1"
  local metrics_file="$results_dir/$gate-control-plane-metrics.tsv"
  local stats_file="$results_dir/$gate-docker-stats.tsv"
  while true; do
    local timestamp
    timestamp="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    curl --fail --silent http://127.0.0.1:38080/internal/metrics 2>/dev/null | \
      grep -E '^(go_goroutines|go_memory_alloc_bytes|database_pool_connections|db_pool_|postgres_available|redis_available|p2p_rooms_active|relay_allocations_active|relay_migrations_total|relay_migration_failed_total|relay_node_(state|active_allocations|goroutines|memory_bytes|control_connected|control_reconnects_total))' | \
      awk -v timestamp="$timestamp" '{print timestamp "\t" $0}' >>"$metrics_file" || true
    docker stats --no-stream --format '{{json .}}' $("${compose[@]}" ps --quiet) 2>/dev/null | \
      awk -v timestamp="$timestamp" '{print timestamp "\t" $0}' >>"$stats_file" || true
    sleep 60
  done
}

inject_midrun_dependency_restarts() {
  sleep 10800
  record_event restart redis
  "${compose[@]}" restart redis
  wait_ready
  sleep 120
  record_event restart control-plane
  "${compose[@]}" restart control-plane
  wait_ready
}

inject_hourly_relay_rotation() {
  local hour service
  for hour in $(seq 1 23); do
    sleep 3600
    if (( hour % 2 == 1 )); then service=edge-relay-a; else service=edge-relay-b; fi
    record_event relay_rotation "hour=$hour service=$service"
    "${compose[@]}" restart "$service"
    wait_ready
  done
}

record_residuals() {
  local gate="$1"
  local postgres_id
  postgres_id="$("${compose[@]}" ps --quiet postgres)"
  docker exec "$postgres_id" psql -At -U projectrebound -d projectrebound -F $'\t' -c \
    "SELECT 'active_rooms', COUNT(*) FROM p2p_rooms WHERE state <> 'CLOSED'
     UNION ALL SELECT 'active_allocations', COUNT(*) FROM relay_allocations WHERE state IN ('ALLOCATED','BINDING','ACTIVE','MIGRATING')
     UNION ALL SELECT 'failed_migrations', COUNT(*) FROM relay_migrations WHERE state = 'FAILED'
     UNION ALL SELECT 'duplicate_players', COUNT(*) FROM (SELECT steam_id FROM players GROUP BY steam_id HAVING COUNT(*) > 1) duplicated
     UNION ALL SELECT 'duplicate_active_session_families', COUNT(*) FROM (SELECT token_family_id FROM auth_sessions WHERE revoked_at IS NULL GROUP BY token_family_id HAVING COUNT(*) > 1) duplicated;" \
    >"$results_dir/$gate-residuals.tsv"
}

validate_residuals() {
  local gate="$1"
  if awk -F '\t' '$2 != 0 { exit 1 }' "$results_dir/$gate-residuals.tsv"; then
    record_event residuals_ok "$gate"
  else
    echo "Residual resource validation failed for $gate" >&2
    cat "$results_dir/$gate-residuals.tsv" >&2
    return 1
  fi
}

run_gate() {
  local gate="$1" scenario="$2" min_seconds="$3" clients="$4" rooms="$5" relay_connections="$6" injector="${7:-}"
  local report="$artifacts_dir/$gate.json" prometheus="$artifacts_dir/$gate.prom" log="$results_dir/$gate.log"
  local load_name="$project-load-$gate"
  local load_pid telemetry_pid injector_pid="" injector_status=0 run_status=0

  reset_stack "$gate"
  write_status RUNNING "$gate"
  record_event gate_started "$gate"
  docker run --rm --name "$load_name" --network "${project}_longrun" \
    --volume "$scenario:/scenario.yaml:ro" \
    --volume "$artifacts_dir:/results" \
    "$V11_LOAD_BOT_IMAGE" -config /scenario.yaml -report "/results/$gate.json" \
    -prometheus-report "/results/$gate.prom" >"$log" 2>&1 &
  load_pid=$!
  collect_telemetry "$gate" &
  telemetry_pid=$!
  if [[ -n "$injector" ]]; then
    (
      trap - ERR
      if ! "$injector"; then
        record_event injector_failed "gate=$gate"
        docker rm --force "$load_name" >/dev/null 2>&1 || true
        exit 1
      fi
    ) &
    injector_pid=$!
  fi

  wait "$load_pid" || run_status=$?
  kill "$telemetry_pid" >/dev/null 2>&1 || true
  wait "$telemetry_pid" >/dev/null 2>&1 || true
  if [[ -n "$injector_pid" ]]; then
    if kill -0 "$injector_pid" >/dev/null 2>&1; then
      kill "$injector_pid" >/dev/null 2>&1 || true
      wait "$injector_pid" >/dev/null 2>&1 || true
    else
      wait "$injector_pid" || injector_status=$?
    fi
  fi
  record_event load_bot_exit "gate=$gate status=$run_status"
  if (( injector_status != 0 )); then
    echo "Fault injector failed for $gate with status $injector_status" >&2
    return "$injector_status"
  fi
  sleep 20
  python3 "$script_dir/validate-report.py" "$report" "$min_seconds" "$clients" "$rooms" "$relay_connections" | tee "$results_dir/$gate-validation.txt"
  python3 "$script_dir/validate-telemetry.py" "$results_dir/$gate-control-plane-metrics.tsv" "$min_seconds" | tee "$results_dir/$gate-telemetry-validation.txt"
  record_residuals "$gate"
  validate_residuals "$gate"
  record_event gate_passed "$gate"
}

on_error() {
  local status=$?
  write_status FAILED "${current_gate:-unknown}"
  record_event runner_failed "gate=${current_gate:-unknown} status=$status"
  exit "$status"
}
trap on_error ERR

write_status PREPARING none
record_event runner_started "$project"
for image in "$V11_CONTROL_PLANE_IMAGE" "$V11_EDGE_RELAY_IMAGE" "$V11_LOAD_BOT_IMAGE"; do
  if ! docker image inspect "$image" >/dev/null 2>&1; then
    docker pull "$image"
  fi
done

current_gate=preflight-3m
run_gate "$current_gate" "$results_dir/scenarios/preflight-3m.yaml" 180 100 30 20

current_gate=basic-1h
run_gate "$current_gate" "$results_dir/scenarios/basic-1h.yaml" 3600 100 30 20

current_gate=full-6h
run_gate "$current_gate" "$results_dir/scenarios/full-6h.yaml" 21600 300 100 100 inject_midrun_dependency_restarts

current_gate=relay-soak-24h
run_gate "$current_gate" "$results_dir/scenarios/relay-soak-24h.yaml" 86400 200 100 100 inject_hourly_relay_rotation

write_status COMPLETE none
record_event runner_completed "$project"
if [[ "${V11_LONGRUN_KEEP_STACK:-1}" != "1" ]]; then
  "${compose[@]}" down --volumes --remove-orphans
fi
