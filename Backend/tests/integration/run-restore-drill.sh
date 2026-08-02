#!/usr/bin/env bash
set -euo pipefail

if [[ "${RESTORE_DRILL_I_UNDERSTAND:-}" != "disposable-postgres-containers" ]]; then
  echo "Refusing to run: set RESTORE_DRILL_I_UNDERSTAND=disposable-postgres-containers." >&2
  exit 2
fi
if [[ "${PROJECTREBOUND_ENVIRONMENT:-test}" == "production" ]]; then
  echo "Refusing to run a restore drill in production." >&2
  exit 2
fi

for command_name in docker go age age-keygen base64 cmp curl openssl pg_dump pg_restore psql sha256sum; do
  command -v "$command_name" >/dev/null || { echo "$command_name is required" >&2; exit 2; }
done

integration_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
backend_dir="$(CDPATH= cd -- "$integration_dir/../.." && pwd)"
work_dir="$(mktemp -d "${TMPDIR:-/tmp}/project-rebound-restore-XXXXXXXX")"
suffix="$(basename "$work_dir" | tr -cd 'a-zA-Z0-9-' | tail -c 18)"
source_name="project-rebound-restore-source-$suffix"
target_name="project-rebound-restore-target-$suffix"
redis_name="project-rebound-restore-redis-$suffix"
control_name="project-rebound-restore-control-$suffix"
relay_name="project-rebound-restore-relay-$suffix"
network_name="project-rebound-restore-$suffix"
relay_volume="project-rebound-restore-relay-$suffix"
control_image="project-rebound/control-plane:restore-$suffix"
relay_image="project-rebound/edge-relay:restore-$suffix"
password="restore-drill-postgres-only"

cleanup() {
  docker rm -f "$relay_name" "$control_name" "$redis_name" "$source_name" "$target_name" >/dev/null 2>&1 || true
  docker volume rm "$relay_volume" >/dev/null 2>&1 || true
  docker network rm "$network_name" >/dev/null 2>&1 || true
  docker image rm "$control_image" "$relay_image" >/dev/null 2>&1 || true
  case "$work_dir" in
    /tmp/project-rebound-restore-*|/var/tmp/project-rebound-restore-*) rm -rf -- "$work_dir" ;;
    *) echo "Refusing to remove unexpected restore work directory: $work_dir" >&2 ;;
  esac
}
on_exit() {
  status=$?
  cleanup
  if [[ -n "${RESTORE_DRILL_RESULT_FILE:-}" ]]; then
    printf '%s\n' "$status" > "$RESTORE_DRILL_RESULT_FILE"
  fi
}
trap on_exit EXIT
trap 'exit 130' HUP INT TERM

start_postgres() {
  local container_name="$1"
  docker run -d --name "$container_name" \
    --network "$network_name" \
    -e POSTGRES_DB=projectrebound \
    -e POSTGRES_USER=projectrebound \
    -e POSTGRES_PASSWORD="$password" \
    -p 127.0.0.1::5432 \
    postgres:17-alpine >/dev/null
  for _ in $(seq 1 60); do
    if docker exec "$container_name" pg_isready -U projectrebound -d projectrebound >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  echo "PostgreSQL container $container_name did not become ready" >&2
  return 1
}

docker network create "$network_name" >/dev/null

database_url() {
  local container_name="$1"
  local port
  port="$(docker port "$container_name" 5432/tcp | awk -F: 'NR == 1 { print $NF }')"
  [[ "$port" =~ ^[0-9]+$ ]] || { echo "invalid mapped PostgreSQL port" >&2; return 1; }
  printf 'postgres://projectrebound:%s@127.0.0.1:%s/projectrebound?sslmode=disable\n' "$password" "$port"
}

random_seed() { head -c 32 /dev/urandom | base64 | tr -d '\n'; }

start_control_plane() {
  local database_container="$1"
  docker rm -f "$control_name" >/dev/null 2>&1 || true
  docker run -d --name "$control_name" \
    --network "$network_name" \
    -p 127.0.0.1::8080 \
    -v "$work_dir/updates:/restore-updates:ro" \
    -e CONTROL_PLANE_ENVIRONMENT=development \
    -e CONTROL_PLANE_HTTP_ADDR=:8080 \
    -e "DATABASE_URL=postgres://projectrebound:$password@$database_container:5432/projectrebound?sslmode=disable" \
    -e "REDIS_ADDRESS=$redis_name:6379" \
    -e CORS_ALLOWED_ORIGINS=http://127.0.0.1 \
    -e ACCESS_TOKEN_KEY_ID=restore-access \
    -e "ACCESS_TOKEN_PRIVATE_KEY_BASE64=$ACCESS_TOKEN_PRIVATE_KEY_BASE64" \
    -e "ADMIN_TOKENS=restore=$admin_token" \
    -e ADMIN_TRUSTED_CIDRS=0.0.0.0/0,::/0 \
    -e RELAY_CONTROL_ADDR=:9090 \
    -e "RELAY_CONTROL_SERVER_NAMES=$control_name" \
    -e "RELAY_BOOTSTRAP_TOKENS=restore=$relay_bootstrap_token" \
    -e "RELAY_CA_CERT_PEM_BASE64=$RELAY_CA_CERT_PEM_BASE64" \
    -e "RELAY_CA_KEY_PEM_BASE64=$RELAY_CA_KEY_PEM_BASE64" \
    -e RELAY_TOKEN_KEY_ID=restore-relay \
    -e "RELAY_TOKEN_PRIVATE_KEY_BASE64=$RELAY_TOKEN_PRIVATE_KEY_BASE64" \
    -e UPDATE_CDN_BASE_URL=https://cdn.invalid/project-rebound \
    -e UPDATE_MANIFEST_DIRECTORY=/restore-updates \
    -e UPDATE_SIGNING_KEY_ID=restore-update \
    -e "UPDATE_SIGNING_PRIVATE_KEY_BASE64=$UPDATE_SIGNING_PRIVATE_KEY_BASE64" \
    -e UPDATE_REALTIME_URL=ws://127.0.0.1/v1/realtime/connect \
    -e UPDATE_STUN_SERVERS=stun:stun.invalid:3478 \
    "$control_image" >/dev/null
  control_http_port="$(docker port "$control_name" 8080/tcp | awk -F: 'NR == 1 { print $NF }')"
  [[ "$control_http_port" =~ ^[0-9]+$ ]] || { echo "invalid Control Plane HTTP port" >&2; return 1; }
  for _ in $(seq 1 90); do
    if curl -fsS "http://127.0.0.1:$control_http_port/health/ready" >/dev/null 2>&1; then
      return 0
    fi
    sleep 1
  done
  docker logs "$control_name" >&2 || true
  echo "restored Control Plane did not become ready" >&2
  return 1
}

start_restored_relay() {
  cat >"$work_dir/relay.yaml" <<EOF
environment: development
display_name: restore-drill-relay
region: restore
zone: restore-a
provider: restore-drill
software_version: 1.1.0-restore
protocol_version: 2
accept_protocol_v1: false
control_plane_url: http://$control_name:8080
control_addr: $control_name:9090
control_server_name: $control_name
data_dir: /edge-relay-data
listen_addr: ":8443"
metrics_addr: 127.0.0.1:9100
advertised_endpoints:
  - protocol: UDP
    host: 198.18.99.10
    port: 8443
supported_protocols: [UDP]
max_allocations: 100
max_egress_bps: 20000000
heartbeat_seconds: 2
control_disconnect_grace_seconds: 60
allocation_idle_seconds: 30
cookie_ttl_seconds: 5
max_datagram_bytes: 1280
max_payload_bytes: 1200
ip_packets_per_second: 300
bind_init_per_second: 100
bind_proof_per_second: 100
invalid_tokens_per_minute: 100
max_allocations_per_ip: 100
max_ip_rate_states: 1000
max_ingress_packets_per_second: 10000
temporary_ban_seconds: 10
max_ingress_mbps: 20
max_memory_mb: 256
degraded_threshold_percent: 70
reject_new_threshold_percent: 90
nat_rebind_window_seconds: 30
max_token_replay_entries: 1000
EOF
  docker run -d --name "$relay_name" \
    --network "$network_name" \
    -e "EDGE_RELAY_BOOTSTRAP_TOKEN=$relay_bootstrap_token" \
    -v "$work_dir/relay.yaml:/etc/projectrebound/config.edge-relay.yaml:ro" \
    -v "$relay_volume:/edge-relay-data" \
    "$relay_image" >/dev/null
  for _ in $(seq 1 90); do
    if curl -fsS -H "Authorization: Bearer $admin_token" \
      "http://127.0.0.1:$control_http_port/internal/v1/relay-nodes?state=READY&limit=10" | \
      grep -q 'restore-drill-relay'; then
      return 0
    fi
    sleep 1
  done
  docker logs "$relay_name" >&2 || true
  echo "Relay did not register against the restored Control Plane" >&2
  return 1
}

mkdir -p "$work_dir/updates"
cat >"$work_dir/updates/restore.json" <<'JSON'
{
  "schema_version": 1,
  "product": "project-rebound",
  "platform": "windows",
  "architecture": "amd64",
  "channel": "stable",
  "version": "1.1.0",
  "minimum_supported_version": "1.0.0",
  "published_at": "2026-07-21T00:00:00Z",
  "files": [{
    "file_id": "restore_windows_amd64",
    "path": "ProjectRebound.exe",
    "size": 1,
    "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
    "compression": "none",
    "object_key": "restore/windows/ProjectRebound.exe"
  }]
}
JSON

ACCESS_TOKEN_PRIVATE_KEY_BASE64="$(random_seed)"
RELAY_TOKEN_PRIVATE_KEY_BASE64="$(random_seed)"
UPDATE_SIGNING_PRIVATE_KEY_BASE64="$(random_seed)"
admin_token="restore-admin-token-0123456789-abcdef"
relay_bootstrap_token="restore-relay-bootstrap-token-0123456789"
openssl genpkey -algorithm ED25519 -out "$work_dir/relay-ca-key.pem" >/dev/null 2>&1
openssl req -x509 -new -key "$work_dir/relay-ca-key.pem" -out "$work_dir/relay-ca-cert.pem" \
  -days 1 -subj '/CN=Project Rebound Restore Drill CA' >/dev/null 2>&1
RELAY_CA_CERT_PEM_BASE64="$(base64 -w0 "$work_dir/relay-ca-cert.pem")"
RELAY_CA_KEY_PEM_BASE64="$(base64 -w0 "$work_dir/relay-ca-key.pem")"

identity_file="$work_dir/age-identity.txt"
age-keygen -o "$identity_file" >/dev/null 2>&1
recipient="$(age-keygen -y "$identity_file")"

docker build --build-arg "GOPROXY=${GOPROXY:-https://proxy.golang.org,direct}" \
  --build-arg "GOSUMDB=${GOSUMDB:-sum.golang.org}" \
  -f "$backend_dir/deployments/compose/control-plane.Dockerfile" -t "$control_image" "$backend_dir" >/dev/null
docker build --build-arg "GOPROXY=${GOPROXY:-https://proxy.golang.org,direct}" \
  --build-arg "GOSUMDB=${GOSUMDB:-sum.golang.org}" \
  -f "$backend_dir/deployments/relay/Dockerfile" -t "$relay_image" "$backend_dir" >/dev/null

docker run -d --name "$redis_name" --network "$network_name" redis:7-alpine >/dev/null

started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
started_epoch="$(date -u +%s)"
start_postgres "$source_name"
source_url="$(database_url "$source_name")"
(
  cd "$backend_dir"
  TEST_DATABASE_URL="$source_url" go test ./internal/database -run '^TestMigratorAgainstPostgreSQL$' -count=1
)

fixture_player_id="restore-player-0001"
fixture_steam_id="76561198000000001"
docker exec "$source_name" psql -v ON_ERROR_STOP=1 -U projectrebound -d projectrebound -c \
  "INSERT INTO players (id, steam_id, persona_name, created_at, updated_at) VALUES ('$fixture_player_id', '$fixture_steam_id', 'Restore Fixture', NOW(), NOW())" >/dev/null
docker exec -i "$source_name" psql -v ON_ERROR_STOP=1 -U projectrebound -d projectrebound <<'SQL' >/dev/null
INSERT INTO players (id, steam_id, persona_name, created_at, updated_at)
VALUES ('restore-peer-0001', '76561198000000002', 'Restore Peer', NOW(), NOW());
INSERT INTO p2p_rooms (
  id, host_player_id, host_token_hash, display_name, region, mode, version,
  max_players, player_count, state, last_heartbeat_at, created_at, updated_at
) VALUES (
  'restore-room-0001', 'restore-player-0001', decode(repeat('11', 32), 'hex'),
  'Restore Active Room', 'restore', 'drill', '1.1.0', 2, 2, 'RUNNING', NOW(), NOW(), NOW()
);
INSERT INTO p2p_room_members (room_id, player_id, role, status, joined_at)
VALUES
  ('restore-room-0001', 'restore-player-0001', 'HOST', 'ACTIVE', NOW()),
  ('restore-room-0001', 'restore-peer-0001', 'MEMBER', 'ACTIVE', NOW());
INSERT INTO connections (
  id, room_id, host_player_id, peer_player_id, state, selected_path,
  expires_at, created_at, updated_at
) VALUES (
  'restore-connection-0001', 'restore-room-0001', 'restore-player-0001',
  'restore-peer-0001', 'CONNECTED', 'UDP_RELAY', NOW() + INTERVAL '1 hour', NOW(), NOW()
);
INSERT INTO relay_nodes (
  id, display_name, region, zone, provider, state, software_version, protocol_version,
  public_endpoints, supported_protocols, max_allocations, max_egress_bps,
  active_allocations, current_egress_bps, current_ingress_bps,
  certificate_fingerprint, certificate_expires_at, node_token_hash,
  last_heartbeat_at, lease_expires_at, created_at, updated_at
) VALUES (
  'restore-relay-0001', 'Restore Relay', 'restore', 'restore-a', 'drill', 'READY',
  '1.1.0', 2, '[{"protocol":"udp","host":"198.51.100.10","port":8443}]'::jsonb,
  ARRAY['UDP'], 100, 100000000, 1, 1024, 1024, repeat('a', 64),
  NOW() + INTERVAL '7 days', decode(repeat('22', 32), 'hex'), NOW(),
  NOW() + INTERVAL '1 minute', NOW(), NOW()
);
INSERT INTO relay_allocations (
  id, connection_id, room_id, relay_node_id, state, protocol,
  max_bps, max_pps, max_total_bytes, expires_at, created_at, updated_at
) VALUES (
  'restore-allocation-0001', 'restore-connection-0001', 'restore-room-0001',
  'restore-relay-0001', 'ACTIVE', 'UDP', 256000, 200, 268435456,
  NOW() + INTERVAL '1 hour', NOW(), NOW()
);
SQL

start_control_plane "$source_name"
if ! curl -fsS -H "Authorization: Bearer $admin_token" \
  "http://127.0.0.1:$control_http_port/v1/admin/players/$fixture_player_id" >"$work_dir/player-before.json"; then
  docker logs "$control_name" >&2 || true
  echo "source Control Plane could not read the fixture player through the administrator API" >&2
  exit 1
fi
if ! curl -fsS \
  "http://127.0.0.1:$control_http_port/v1/updates/windows/1.1.0/manifest?architecture=amd64&channel=stable" \
  >"$work_dir/manifest-before.json"; then
  docker logs "$control_name" >&2 || true
  echo "source Control Plane could not serve the signed fixture manifest" >&2
  exit 1
fi
grep -q "$fixture_player_id" "$work_dir/player-before.json"
grep -q '"signature"' "$work_dir/manifest-before.json"
docker rm -f "$control_name" >/dev/null

key_bundle_plain="$work_dir/recovery-keys.env"
key_bundle_encrypted="$work_dir/recovery-keys.env.age"
printf '%s\n' \
  "ACCESS_TOKEN_PRIVATE_KEY_BASE64=$ACCESS_TOKEN_PRIVATE_KEY_BASE64" \
  "RELAY_TOKEN_PRIVATE_KEY_BASE64=$RELAY_TOKEN_PRIVATE_KEY_BASE64" \
  "UPDATE_SIGNING_PRIVATE_KEY_BASE64=$UPDATE_SIGNING_PRIVATE_KEY_BASE64" \
  "RELAY_CA_CERT_PEM_BASE64=$RELAY_CA_CERT_PEM_BASE64" \
  "RELAY_CA_KEY_PEM_BASE64=$RELAY_CA_KEY_PEM_BASE64" \
  "admin_token=$admin_token" \
  "relay_bootstrap_token=$relay_bootstrap_token" \
  >"$key_bundle_plain"
age --recipient "$recipient" --output "$key_bundle_encrypted" "$key_bundle_plain"
key_bundle_sha256="$(sha256sum "$key_bundle_encrypted" | awk '{print $1}')"
rm -f -- "$key_bundle_plain" "$work_dir/relay-ca-key.pem" "$work_dir/relay-ca-cert.pem"
unset ACCESS_TOKEN_PRIVATE_KEY_BASE64 RELAY_TOKEN_PRIVATE_KEY_BASE64 UPDATE_SIGNING_PRIVATE_KEY_BASE64
unset RELAY_CA_CERT_PEM_BASE64 RELAY_CA_KEY_PEM_BASE64 admin_token relay_bootstrap_token

backup_dir="$work_dir/backups"
metrics_dir="$work_dir/metrics"
backup_output="$(
  cd "$backend_dir"
  DATABASE_URL="$source_url" \
  BACKUP_DIRECTORY="$backup_dir" \
  BACKUP_METRICS_DIRECTORY="$metrics_dir" \
  BACKUP_ENCRYPTION_RECIPIENT="$recipient" \
    bash scripts/backup/postgres-backup.sh
)"
backup_file="${backup_output#BACKUP_OK }"
[[ -f "$backup_file" && -f "$backup_file.sha256" ]] || { echo "backup artifacts are missing" >&2; exit 1; }
backup_sha256="$(sha256sum "$backup_file" | awk '{print $1}')"

(
  cd "$backend_dir"
  BACKUP_AGE_IDENTITY_FILE="$identity_file" \
  BACKUP_METRICS_DIRECTORY="$metrics_dir" \
    bash scripts/backup/verify-backup.sh "$backup_file"
)

start_postgres "$target_name"
target_url="$(database_url "$target_name")"
restore_started_milliseconds="$(date -u +%s%3N)"
(
  cd "$backend_dir"
  DATABASE_URL="$target_url" \
  BACKUP_AGE_IDENTITY_FILE="$identity_file" \
  BACKUP_METRICS_DIRECTORY="$metrics_dir" \
  RESTORE_I_UNDERSTAND=replace-target-database \
    bash scripts/backup/postgres-restore.sh "$backup_file"
)
database_restore_finished_milliseconds="$(date -u +%s%3N)"

age --decrypt --identity "$identity_file" --output "$key_bundle_plain" "$key_bundle_encrypted"
set -a
. "$key_bundle_plain"
set +a
rm -f -- "$key_bundle_plain"

start_control_plane "$target_name"
if ! curl -fsS -H "Authorization: Bearer $admin_token" \
  "http://127.0.0.1:$control_http_port/v1/admin/players/$fixture_player_id" >"$work_dir/player-after.json"; then
  docker logs "$control_name" >&2 || true
  echo "restored Control Plane could not read the fixture player through the administrator API" >&2
  exit 1
fi
if ! curl -fsS \
  "http://127.0.0.1:$control_http_port/v1/updates/windows/1.1.0/manifest?architecture=amd64&channel=stable" \
  >"$work_dir/manifest-after.json"; then
  docker logs "$control_name" >&2 || true
  echo "restored Control Plane could not serve the signed fixture manifest" >&2
  exit 1
fi
grep -q "$fixture_player_id" "$work_dir/player-after.json"
sed -E 's/"request_id":"[^"]+"/"request_id":"normalized"/' \
  "$work_dir/manifest-before.json" >"$work_dir/manifest-before.normalized.json"
sed -E 's/"request_id":"[^"]+"/"request_id":"normalized"/' \
  "$work_dir/manifest-after.json" >"$work_dir/manifest-after.normalized.json"
cmp "$work_dir/manifest-before.normalized.json" "$work_dir/manifest-after.normalized.json"
start_restored_relay
restore_finished_milliseconds="$(date -u +%s%3N)"

restored_player_id="$(docker exec "$target_name" psql -At -U projectrebound -d projectrebound -c "SELECT id FROM players WHERE steam_id = '$fixture_steam_id'")"
latest_migration_file="$(find "$backend_dir/migrations" -maxdepth 1 -type f -name '[0-9][0-9][0-9][0-9][0-9][0-9]_*.sql' -printf '%f\n' | sort | tail -n 1)"
[[ -n "$latest_migration_file" ]] || { echo "no database migrations were found" >&2; exit 1; }
expected_schema_version_text="${latest_migration_file%%_*}"
expected_schema_version="$((10#$expected_schema_version_text))"
schema_version="$(docker exec "$target_name" psql -At -U projectrebound -d projectrebound -c 'SELECT MAX(version) FROM schema_migrations')"
required_table_count="$(docker exec "$target_name" psql -At -U projectrebound -d projectrebound -c "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name = ANY (ARRAY['players','auth_sessions','auth_login_audit_logs','auth_risk_events','auth_login_events','invite_codes','invite_code_uses','game_servers','p2p_rooms','p2p_room_members','connections','connection_candidates','connection_path_checks','relay_bootstrap_tokens','relay_nodes','relay_allocations','relay_node_audit_logs','relay_migrations','relay_signing_keys','relay_keyset_acks','relay_node_credentials','admin_audit_logs'])")"
ephemeral_state="$(docker exec "$target_name" psql -At -F, -U projectrebound -d projectrebound -c "SELECT (SELECT state FROM p2p_rooms WHERE id = 'restore-room-0001'), (SELECT state FROM connections WHERE id = 'restore-connection-0001'), (SELECT state FROM relay_allocations WHERE id = 'restore-allocation-0001'), (SELECT state FROM relay_nodes WHERE id = 'restore-relay-0001'), (SELECT active_allocations FROM relay_nodes WHERE id = 'restore-relay-0001'), (SELECT COUNT(*) FROM p2p_room_members WHERE room_id = 'restore-room-0001' AND status = 'ACTIVE')")"
[[ "$restored_player_id" == "$fixture_player_id" ]] || { echo "fixture player was not restored" >&2; exit 1; }
[[ "$schema_version" == "$expected_schema_version" ]] || { echo "restored schema version is $schema_version, expected $expected_schema_version" >&2; exit 1; }
[[ "$required_table_count" == "22" ]] || { echo "only $required_table_count required tables were restored" >&2; exit 1; }
[[ "$ephemeral_state" == "CLOSED,FAILED,FAILED,OFFLINE,0,0" ]] || { echo "restored ephemeral state was not invalidated: $ephemeral_state" >&2; exit 1; }
grep -q 'projectrebound_backup_last_run_success 1' "$metrics_dir/projectrebound-backup-status.prom"
grep -q 'projectrebound_backup_verification_success 1' "$metrics_dir/projectrebound-backup-verification.prom"
test -f "$metrics_dir/projectrebound-restore-drill.prom"

(
  cd "$backend_dir"
  TEST_DATABASE_URL="$target_url" go test ./internal/database -run '^TestMigratorAgainstPostgreSQL$' -count=1
)

finished_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
printf 'RESTORE_DRILL_OK started_at=%s finished_at=%s rto_milliseconds=%s database_restore_milliseconds=%s schema_version=%s required_tables=%s player_id=%s backup_sha256=%s key_bundle_sha256=%s\n' \
  "$started_at" "$finished_at" "$((restore_finished_milliseconds - restore_started_milliseconds))" \
  "$((database_restore_finished_milliseconds - restore_started_milliseconds))" "$schema_version" "$required_table_count" \
  "$restored_player_id" "$backup_sha256" "$key_bundle_sha256"
printf 'RESTORE_APPLICATION_SMOKE control_plane=ready admin_player=verified manifest_signature=stable relay_registration=ready\n'
printf 'RESTORE_EPHEMERAL_STATE room=closed connection=failed allocation=failed relay=offline active_allocations=0 active_members=0\n'
printf 'RESTORE_DRILL_TOTAL_SECONDS %s\n' "$(( $(date -u +%s) - started_epoch ))"
