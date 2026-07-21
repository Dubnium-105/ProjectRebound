#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM

if CONTROL_PLANE_IMAGE=ghcr.io/example/control-plane:latest \
  CONTROL_PLANE_ENV_FILE="$temporary_dir/missing.env" \
  "$script_dir/release/preflight.sh" >/dev/null 2>&1; then
  echo 'Preflight accepted a missing configuration and floating image.' >&2
  exit 1
fi

mkdir -p "$temporary_dir/scripts/release"
cp "$script_dir/release/rollback.sh" "$temporary_dir/scripts/release/rollback.sh"
cat >"$temporary_dir/scripts/deploy-control-plane.sh" <<'EOF'
#!/usr/bin/env bash
set -eu
test "${DEPLOY_SOURCE:-}" = ci
printf '%s\n' "${CONTROL_PLANE_IMAGE:?}" >"${ROLLBACK_TEST_LOG:?}"
EOF
cat >"$temporary_dir/scripts/verify-control-plane.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
chmod +x "$temporary_dir/scripts/"*.sh "$temporary_dir/scripts/release/rollback.sh"

if ROLLBACK_TEST_LOG="$temporary_dir/rollback.log" \
  "$temporary_dir/scripts/release/rollback.sh" control-plane ghcr.io/example/control-plane:latest >/dev/null 2>&1; then
  echo 'Rollback accepted latest.' >&2
  exit 1
fi
ROLLBACK_TEST_LOG="$temporary_dir/rollback.log" \
  "$temporary_dir/scripts/release/rollback.sh" control-plane ghcr.io/example/control-plane:1.1.0 >/dev/null
grep -qx 'ghcr.io/example/control-plane:1.1.0' "$temporary_dir/rollback.log"

for required in config-file config-placeholders openapi-schema disk-space object-storage backup-missing image-digest postgres redis migration-state; do
  grep -q "$required" "$script_dir/release/preflight.sh"
done
grep -q 'migrate_existing.*true' "$script_dir/release/rolling-edge-relay.sh"
grep -q 'database_rollback=false' "$script_dir/release/rollback.sh"
printf 'RELEASE_SCRIPT_TEST_OK\n'
