#!/usr/bin/env bash
set -euo pipefail

if [[ "${EUID:-$(id -u)}" -ne 0 ]]; then
  echo "bootstrap-deploy-user.sh must run as root." >&2
  exit 1
fi

deploy_user="${1:?deploy user is required}"
deploy_root="${2:?deploy root is required}"
env_file="${3:?deployment env file is required}"
shift 3

[[ "$deploy_user" =~ ^[a-z_][a-z0-9_-]*$ && "$deploy_user" != "root" ]] || {
  echo "Invalid non-root deploy user." >&2
  exit 1
}
[[ "$deploy_root" =~ ^/(opt|srv|mnt)/[A-Za-z0-9._/-]+$ ]] || {
  echo "Deploy root must be an explicit path below /opt, /srv, or /mnt." >&2
  exit 1
}
[[ "$env_file" =~ ^/[A-Za-z0-9._/-]+$ && -f "$env_file" ]] || {
  echo "Deployment env file is missing or invalid." >&2
  exit 1
}
[[ "$#" -ge 1 ]] || { echo "At least one authorized public key is required." >&2; exit 1; }
getent group docker >/dev/null || { echo "The docker group does not exist." >&2; exit 1; }

if ! id "$deploy_user" >/dev/null 2>&1; then
  useradd --create-home --shell /bin/bash "$deploy_user"
fi
usermod --append --groups docker "$deploy_user"

deploy_group="$(id -gn "$deploy_user")"
deploy_home="$(getent passwd "$deploy_user" | cut -d: -f6)"
[[ "$deploy_home" == /home/* ]] || { echo "Unexpected deploy user home." >&2; exit 1; }

install -d -m 0700 -o "$deploy_user" -g "$deploy_group" "$deploy_home/.ssh"
authorized_keys="$deploy_home/.ssh/authorized_keys"
touch "$authorized_keys"
chown "$deploy_user:$deploy_group" "$authorized_keys"
chmod 0600 "$authorized_keys"

for public_key_file in "$@"; do
  [[ "$public_key_file" =~ ^/tmp/[A-Za-z0-9._-]+\.pub$ && -f "$public_key_file" ]] || {
    echo "Authorized key must be an explicit /tmp/*.pub file." >&2
    exit 1
  }
  public_key="$(tr -d '\r\n' <"$public_key_file")"
  [[ "$public_key" =~ ^ssh-ed25519\ [A-Za-z0-9+/=]+\ github-actions-projectrebound-[A-Za-z0-9._-]+$ ]] || {
    echo "Unexpected authorized key format or comment." >&2
    exit 1
  }
  key_comment="${public_key##* }"
  if ! grep -Fq " $key_comment" "$authorized_keys"; then
    printf 'restrict %s\n' "$public_key" >>"$authorized_keys"
  fi
done

install -d -m 0750 -o "$deploy_user" -g "$deploy_group" \
  "$deploy_root" "$deploy_root/releases" "$deploy_root/backups"
chown -R "$deploy_user:$deploy_group" "$deploy_root"
chown "$deploy_user:$deploy_group" "$env_file"
chmod 0600 "$env_file"

printf 'DEPLOY_USER_READY user=%s root=%s env=%s key_count=%s\n' \
  "$deploy_user" "$deploy_root" "$env_file" "$#"
