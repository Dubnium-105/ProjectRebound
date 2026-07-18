#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM

test_backend="$temporary_dir/Backend"
mkdir -p "$temporary_dir/bin" "$test_backend/scripts" \
  "$test_backend/deployments/control-plane" "$test_backend/deployments/edge-relay"
cp "$script_dir/deploy-control-plane.sh" "$script_dir/deploy-edge-relay.sh" "$test_backend/scripts/"
touch "$test_backend/deployments/control-plane/docker-compose.yaml"
touch "$test_backend/deployments/edge-relay/docker-compose.yaml"
printf 'relay_id: relay-test\n' >"$test_backend/deployments/edge-relay/config.edge-relay.yaml"

docker_log="$temporary_dir/docker.log"
cat >"$temporary_dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${DOCKER_LOG:?}"
case " $* " in
  *" ps --status running -q edge-relay "*) printf 'fake-container\n' ;;
  *" logs "*) printf 'relay control connected\n' ;;
esac
exit 0
EOF
cat >"$temporary_dir/bin/curl" <<'EOF'
#!/usr/bin/env bash
printf '{"status":"ready"}\n'
EOF
chmod +x "$temporary_dir/bin/docker" "$temporary_dir/bin/curl"

control_env="$temporary_dir/control.env"
printf 'CONTROL_PLANE_ADMIN_PORT=18080\n' >"$control_env"
edge_env="$temporary_dir/edge.env"
printf 'EDGE_RELAY_BOOTSTRAP_TOKEN=\n' >"$edge_env"
control_image="ghcr.io/example/projectrebound-control-plane:sha-1111111111111111111111111111111111111111"
edge_image="ghcr.io/example/projectrebound-edge-relay:sha-2222222222222222222222222222222222222222"

if CONTROL_PLANE_ENV_FILE="$control_env" DEPLOY_SOURCE=ci CONTROL_PLANE_IMAGE=invalid \
  bash "$test_backend/scripts/deploy-control-plane.sh" >/dev/null 2>&1; then
  echo "Expected an invalid control-plane CI image to be rejected" >&2
  exit 1
fi

: >"$docker_log"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" \
  CONTROL_PLANE_ENV_FILE="$control_env" DEPLOY_SOURCE=ci CONTROL_PLANE_IMAGE="$control_image" \
  bash "$test_backend/scripts/deploy-control-plane.sh" >/dev/null
grep -q ' pull$' "$docker_log"
! grep -q ' build ' "$docker_log"

: >"$docker_log"
PATH="$temporary_dir/bin:$PATH" DOCKER_LOG="$docker_log" \
  CONTROL_PLANE_ENV_FILE="$control_env" DEPLOY_SOURCE=source \
  bash "$test_backend/scripts/deploy-control-plane.sh" >/dev/null
grep -q ' build --pull control-plane$' "$docker_log"

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
  EDGE_RELAY_ENV_FILE="$edge_env" DEPLOY_SOURCE=source \
  bash "$test_backend/scripts/deploy-edge-relay.sh" >/dev/null
grep -q ' build --pull edge-relay$' "$docker_log"

printf 'DEPLOY_SOURCE_TEST_OK\n'
