#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
temporary_dir="$(mktemp -d)"
process_id="$$"
release_one="cd-test-${process_id}-one"
release_two="cd-test-${process_id}-two"
bundle_one="/tmp/projectrebound-${release_one}.tar.gz"
bundle_two="/tmp/projectrebound-${release_two}.tar.gz"
trap 'rm -rf "$temporary_dir"; rm -f "$bundle_one" "$bundle_two"' EXIT HUP INT TERM

mkdir -p "$temporary_dir/bin" "$temporary_dir/deploy"
cat >"$temporary_dir/bin/docker" <<'EOF'
#!/usr/bin/env bash
case "${1:-}" in
  info) exit 0 ;;
  ps) exit 0 ;;
  *) exit 0 ;;
esac
EOF
chmod +x "$temporary_dir/bin/docker"
control_env="$temporary_dir/control-plane.env"
printf 'test-only=true\n' >"$control_env"

make_bundle() {
  local release_id="$1"
  local bundle="$2"
  local staging="$temporary_dir/staging-$release_id"
  mkdir -p "$staging/Backend/scripts" "$staging/Backend/deploy"
  cat >"$staging/Backend/scripts/deploy-control-plane.sh" <<EOF
#!/usr/bin/env bash
set -eu
case "\${BASH_SOURCE[0]}" in
  *${release_two}*) exit 42 ;;
esac
test "\${DEPLOY_PULL_ONLY:-}" = "1"
test -n "\${CONTROL_PLANE_IMAGE:-}"
EOF
  cat >"$staging/Backend/scripts/verify-control-plane.sh" <<'EOF'
#!/usr/bin/env bash
set -eu
test -n "${PUBLIC_BASE_URL:-}"
EOF
  cat >"$staging/Backend/scripts/backup-control-plane.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  cat >"$staging/Backend/deploy/deploy.sh" <<'EOF'
#!/usr/bin/env bash
exit 0
EOF
  tar -C "$staging" -czf "$bundle" Backend
}

image_one="ghcr.io/example/projectrebound-control-plane:sha-1111111111111111111111111111111111111111"
image_two="ghcr.io/example/projectrebound-control-plane:sha-2222222222222222222222222222222222222222"
make_bundle "$release_one" "$bundle_one"
PATH="$temporary_dir/bin:$PATH" bash "$script_dir/remote-deploy.sh" \
  control-plane "$temporary_dir/deploy" "$release_one" "$bundle_one" "$image_one" \
  "$control_env" /dev/null /dev/null http://127.0.0.1:8080 0 >/dev/null

current_link="$temporary_dir/deploy/current-control-plane"
test "$(readlink -f "$current_link")" = "$temporary_dir/deploy/releases/$release_one"
test "$(cat "$temporary_dir/deploy/releases/$release_one/.deployed-image")" = "$image_one"
test ! -e "$bundle_one"

make_bundle "$release_two" "$bundle_two"
rollback_log="$temporary_dir/rollback.log"
if PATH="$temporary_dir/bin:$PATH" bash "$script_dir/remote-deploy.sh" \
  control-plane "$temporary_dir/deploy" "$release_two" "$bundle_two" "$image_two" \
  "$control_env" /dev/null /dev/null http://127.0.0.1:8080 0 \
  >"$temporary_dir/unexpected-success.log" 2>"$rollback_log"; then
  echo "Expected the second release to fail" >&2
  exit 1
fi
grep -q 'ROLLBACK_OK' "$rollback_log"
test "$(readlink -f "$current_link")" = "$temporary_dir/deploy/releases/$release_one"
test ! -e "$bundle_two"
test ! -e "$temporary_dir/deploy/releases/$release_two/.deployed-image"

printf 'REMOTE_DEPLOY_TEST_OK\n'
