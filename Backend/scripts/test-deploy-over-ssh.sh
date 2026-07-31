#!/usr/bin/env bash
set -euo pipefail

script_dir="$(CDPATH= cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM

test_repo="$temporary_dir/repository"
mkdir -p "$test_repo/Backend/scripts" "$test_repo/Backend/deployments" \
  "$test_repo/AdminWeb" "$temporary_dir/bin"
cp "$script_dir/deploy-over-ssh.sh" "$script_dir/remote-deploy.sh" \
  "$test_repo/Backend/scripts/"

cat >"$temporary_dir/bin/ssh" <<'EOF'
#!/usr/bin/env bash
printf '%s\n' "$*" >>"${SSH_TEST_LOG:?}"
case "$*" in
  *"docker login"*) cat >/dev/null ;;
esac
EOF

cat >"$temporary_dir/bin/scp" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
archive=""
remote_script=""
for argument in "$@"; do
  case "$argument" in
    *.tar.gz) archive="$argument" ;;
    *-remote-deploy.sh) remote_script="$argument" ;;
  esac
done
[[ -f "$archive" ]]
[[ -f "$remote_script" ]]
tar -tzf "$archive" | grep -q '^Backend/deployments/'
tar -tzf "$archive" | grep -q '^Backend/scripts/deploy-over-ssh.sh$'
tar -tzf "$archive" | grep -q '^AdminWeb/'
if tar -tzf "$archive" | grep -q '^Backend/deploy/'; then
  echo "Legacy Backend/deploy content must not be bundled." >&2
  exit 1
fi
printf 'bundle-ok\n' >"${SCP_TEST_LOG:?}"
EOF
chmod +x "$temporary_dir/bin/ssh" "$temporary_dir/bin/scp"

PATH="$temporary_dir/bin:$PATH" \
SCP_TEST_LOG="$temporary_dir/scp.log" \
SSH_TEST_LOG="$temporary_dir/ssh.log" \
DEPLOY_TARGET=control-plane \
DEPLOY_HOST=deploy.example.test \
DEPLOY_PORT=22 \
DEPLOY_USER=projectrebound-deploy \
DEPLOY_ROOT=/opt/projectrebound-deploy \
DEPLOY_RELEASE_ID=sha-1111111111111111111111111111111111111111-test \
DEPLOY_IMAGE=ghcr.io/example/projectrebound-control-plane:sha-1111111111111111111111111111111111111111 \
CONTROL_PLANE_ENV_FILE=/dev/null \
PUBLIC_BASE_URL=https://api.example.test \
GHCR_USERNAME=example \
GHCR_TOKEN=test-only-token \
  bash "$test_repo/Backend/scripts/deploy-over-ssh.sh"

grep -qx 'bundle-ok' "$temporary_dir/scp.log"
grep -q 'projectrebound-sha-1111111111111111111111111111111111111111-test-remote-deploy.sh' "$temporary_dir/ssh.log"
! grep -q 'bash -s' "$temporary_dir/ssh.log"
printf 'DEPLOY_OVER_SSH_TEST_OK\n'
