#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM

test_backend="$temporary_dir/Backend"
mkdir -p "$temporary_dir/bin" "$test_backend/scripts" \
  "$test_backend/deployments/control-plane" "$test_backend/deployments/edge-relay"
cp "$script_dir/deploy-control-plane.sh" "$script_dir/deploy-meta-server.sh" \
  "$script_dir/deploy-edge-relay.sh" "$test_backend/scripts/"
touch "$test_backend/deployments/control-plane/docker-compose.yaml"
touch "$test_backend/deployments/edge-relay/docker-compose.yaml"
printf 'relay_id: relay-test\n' >"$test_backend/deployments/edge-relay/config.edge-relay.yaml"

docker_log="$temporary_dir/docker.log"
cat >"$temporary_dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${DOCKER_LOG:?}"
if [[ "$*" == *"--entrypoint /bin/sh control-plane"* &&
      "${FAIL_TICKET_PREFLIGHT:-0}" == "1" ]]; then
  exit 1
fi
if [[ "$*" == *"sha256sum \"\$TOOLBOX_PUBKEY_PATH\""* ]]; then
  printf '%s\n' "${FAKE_TOOLBOX_PUBKEY_SHA256:?}"
fi
case " $* " in
  *" ps --status running -q edge-relay "*) printf 'fake-container\n' ;;
  *" logs "*) printf 'relay control connected\n' ;;
  *" inspect -f "*) printf 'true\n' ;;
esac
exit 0
EOF
cat >"$temporary_dir/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf '{"status":"ready"}\n'
EOF
cat >"$temporary_dir/bin/uname" <<'EOF'
#!/usr/bin/env bash
printf 'Linux\n'
EOF
chmod +x "$temporary_dir/bin/docker" "$temporary_dir/bin/curl" "$temporary_dir/bin/uname"

control_env="$temporary_dir/control.env"
toolbox_pubkey="$temporary_dir/signer.pem"
printf '%s\n' '-----BEGIN PUBLIC KEY-----' 'dGVzdA==' '-----END PUBLIC KEY-----' >"$toolbox_pubkey"
expected_toolbox_pubkey_sha256="$(sha256sum "$toolbox_pubkey" | awk '{print $1}')"
export FAKE_TOOLBOX_PUBKEY_SHA256="$expected_toolbox_pubkey_sha256"
printf 'CONTROL_PLANE_ADMIN_PORT=18080\nMETA_SERVER_HTTP_PORT=18082\nTOOLBOX_PUBKEY_HOST_PATH=%s\n' \
  "$toolbox_pubkey" >"$control_env"
control_override="$temporary_dir/docker-compose.production.yaml"
printf 'services: {}\n' >"$control_override"
edge_env="$temporary_dir/edge.env"
printf 'EDGE_RELAY_BOOTSTRAP_TOKEN=\n' >"$edge_env"
control_image="ghcr.io/example/projectrebound-control-plane:sha-1111111111111111111111111111111111111111"
edge_image="ghcr.io/example/projectrebound-edge-relay:sha-2222222222222222222222222222222222222222"
meta_image="ghcr.io/example/projectrebound-meta-server:sha-3333333333333333333333333333333333333333"

if CONTROL_PLANE_ENV_FILE="$control_env" DEPLOY_SOURCE=ci CONTROL_PLANE_IMAGE=invalid \
  bash "$test_backend/scripts/deploy-control-plane.sh" >/dev/null 2>&1; then
  echo "Expected an invalid control-plane CI image to be rejected" >&2
  exit 1
fi

: >"$docker_log"
control_output="$temporary_dir/control.out"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" \
  CONTROL_PLANE_ENV_FILE="$control_env" CONTROL_PLANE_COMPOSE_OVERRIDE_FILE="$control_override" \
  DEPLOY_SOURCE=ci CONTROL_PLANE_IMAGE="$control_image" \
  bash "$test_backend/scripts/deploy-control-plane.sh" >"$control_output"
grep -qx "CONTROL_PLANE_TOOLBOX_PUBKEY_SHA256 $expected_toolbox_pubkey_sha256" "$control_output"
grep -q ' pull$' "$docker_log"
grep -Fq -- "-f $control_override --profile monitoring pull" "$docker_log"
grep -Fq -- 'run --rm -T --no-deps --entrypoint /bin/sh control-plane -c' "$docker_log"
! grep -q ' build ' "$docker_log"

if PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" FAIL_TICKET_PREFLIGHT=1 \
  CONTROL_PLANE_ENV_FILE="$control_env" DEPLOY_SOURCE=ci CONTROL_PLANE_IMAGE="$control_image" \
  bash "$test_backend/scripts/deploy-control-plane.sh" >/dev/null 2>&1; then
  echo "Expected a failed Steam ticket verifier preflight to stop deployment" >&2
  exit 1
fi

: >"$docker_log"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" \
  CONTROL_PLANE_ENV_FILE="$control_env" DEPLOY_SOURCE=ci \
  CONTROL_PLANE_IMAGE="ghcr.io/example/projectrebound-control-plane:1.1.0" \
  bash "$test_backend/scripts/deploy-control-plane.sh" >/dev/null
grep -q ' pull$' "$docker_log"

: >"$docker_log"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" \
  CONTROL_PLANE_ENV_FILE="$control_env" DEPLOY_SOURCE=source \
  bash "$test_backend/scripts/deploy-control-plane.sh" >/dev/null
grep -q ' build --pull control-plane admin-web$' "$docker_log"

if CONTROL_PLANE_ENV_FILE="$control_env" DEPLOY_SOURCE=ci META_SERVER_IMAGE=invalid \
  bash "$test_backend/scripts/deploy-meta-server.sh" >/dev/null 2>&1; then
  echo "Expected an invalid MetaServer CI image to be rejected" >&2
  exit 1
fi

: >"$docker_log"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" \
  CONTROL_PLANE_ENV_FILE="$control_env" CONTROL_PLANE_COMPOSE_OVERRIDE_FILE="$control_override" \
  DEPLOY_SOURCE=ci META_SERVER_IMAGE="$meta_image" \
  bash "$test_backend/scripts/deploy-meta-server.sh" >/dev/null
grep -q ' pull meta-server$' "$docker_log"
grep -Fq -- "-f $control_override --profile meta pull meta-server" "$docker_log"
grep -q ' run --rm --no-deps meta-postgres-provision$' "$docker_log"
grep -q ' run --rm --no-deps meta-redis-provision$' "$docker_log"
grep -q ' up -d --no-deps meta-server$' "$docker_log"
! grep -q ' run --rm meta-postgres-provision$' "$docker_log"
! grep -q ' up .*control-plane' "$docker_log"

: >"$docker_log"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" \
  CONTROL_PLANE_ENV_FILE="$control_env" DEPLOY_SOURCE=source \
  bash "$test_backend/scripts/deploy-meta-server.sh" >/dev/null
grep -q ' build --pull meta-server$' "$docker_log"

if EDGE_RELAY_ENV_FILE="$edge_env" DEPLOY_SOURCE=ci EDGE_RELAY_IMAGE=invalid \
  bash "$test_backend/scripts/deploy-edge-relay.sh" >/dev/null 2>&1; then
  echo "Expected an invalid edge-relay CI image to be rejected" >&2
  exit 1
fi

: >"$docker_log"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" \
  EDGE_RELAY_ENV_FILE="$edge_env" DEPLOY_SOURCE=ci EDGE_RELAY_IMAGE="$edge_image" \
  bash "$test_backend/scripts/deploy-edge-relay.sh" >/dev/null
grep -q ' pull edge-relay$' "$docker_log"
! grep -q ' build ' "$docker_log"

: >"$docker_log"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" \
  EDGE_RELAY_ENV_FILE="$edge_env" DEPLOY_SOURCE=ci \
  EDGE_RELAY_IMAGE="ghcr.io/example/projectrebound-edge-relay@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" \
  bash "$test_backend/scripts/deploy-edge-relay.sh" >/dev/null
grep -q ' pull edge-relay$' "$docker_log"

: >"$docker_log"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" \
  EDGE_RELAY_ENV_FILE="$edge_env" DEPLOY_SOURCE=source \
  bash "$test_backend/scripts/deploy-edge-relay.sh" >/dev/null
grep -q ' build --pull edge-relay$' "$docker_log"

: >"$docker_log"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" EDGE_RELAY_RUNTIME=raw-docker \
  EDGE_RELAY_ENV_FILE="$edge_env" DEPLOY_SOURCE=ci EDGE_RELAY_IMAGE="$edge_image" \
  bash "$test_backend/scripts/deploy-edge-relay.sh" >/dev/null
grep -q "pull $edge_image" "$docker_log"
grep -q 'volume create project-rebound-edge-relay-data' "$docker_log"
grep -q -- '--name project-rebound-edge-relay' "$docker_log"
grep -q -- '--mount type=volume,src=project-rebound-edge-relay-data,dst=/edge-relay-data' "$docker_log"
! grep -q ' compose ' "$docker_log"

grep -q 'ca-certificates.crt' "$script_dir/../deployments/relay/Dockerfile"

provision_script="$script_dir/../deployments/control-plane/provision-meta-postgres.sh"
meta_server_source="$script_dir/../internal/metaserver/server.go"
provision_version="$(sed -n 's/.*WHERE version = \([0-9][0-9]*\).*/\1/p' "$provision_script" | head -n 1)"
server_version="$(sed -n 's/.*WHERE version = \([0-9][0-9]*\).*/\1/p' "$meta_server_source" | head -n 1)"
test -n "$provision_version"
test "$provision_version" = "$server_version"
for table in battlelog_matches battlelog_teams battlelog_participants \
  battlelog_participant_stats battlelog_rounds battlelog_score_breakdowns; do
  grep -Fq "('$table')" "$provision_script"
done
grep -Fq "('players', 'id, steam_id, auth_level, account_status, is_vip')" "$provision_script"
grep -Fq "('auth_sessions', 'id, player_id, token_version, auth_provider, auth_level, steam_verified, pem_fingerprint, integrity_trusted, device_id_hash, device_fingerprint_id, expires_at, revoked_at, revoked_reason, last_used_at')" "$provision_script"
grep -Fq "'GRANT UPDATE (last_used_at) ON TABLE auth_sessions TO %I'" "$provision_script"

verify_script="$script_dir/verify-control-plane.sh"
! grep -Eq 'curl .*\|[[:space:]]*grep .*q' "$verify_script"
grep -Fq 'for _ in {1..30}' "$verify_script"
grep -Fq 'response="$(curl -fsS "$url" 2>/dev/null)"' "$verify_script"

printf 'DEPLOY_SOURCE_TEST_OK\n'
