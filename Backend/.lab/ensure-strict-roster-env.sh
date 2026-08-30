#!/usr/bin/env bash
set -euo pipefail

env_file="${1:?control-plane env file is required}"
enabled="${2:?strict-roster state is required}"
locked_game_sha256="${3:?locked game SHA-256 is required}"

[[ "$env_file" == /opt/projectrebound-deploy/shared/control-plane.env ]] || {
  echo "Refusing unexpected production env path." >&2
  exit 1
}
[[ "$enabled" == true || "$enabled" == false ]] || {
  echo "Strict-roster state must be true or false." >&2
  exit 1
}
[[ "$locked_game_sha256" =~ ^[0-9a-f]{64}$ ]] || {
  echo "Locked game SHA-256 is invalid." >&2
  exit 1
}
[[ -f "$env_file" && ! -L "$env_file" ]] || {
  echo "Production env file is missing or is a symbolic link." >&2
  exit 1
}
command -v openssl >/dev/null 2>&1 || {
  echo "openssl is required." >&2
  exit 1
}

umask 077
env_dir="$(dirname -- "$env_file")"
timestamp="$(date -u +%Y%m%dT%H%M%SZ)"
backup_file="${env_file}.pre-strict-roster-${timestamp}.bak"
[[ ! -e "$backup_file" ]] || {
  echo "Production env backup already exists." >&2
  exit 1
}

work_file="$(mktemp "$env_dir/.control-plane.env.strict-roster.XXXXXX")"
next_file="${work_file}.next"
private_key_file="$(mktemp "$env_dir/.match-admission-key.XXXXXX")"
decoded_key_file="${private_key_file}.decoded"
cleanup() {
  rm -f -- "$work_file" "$next_file" "$private_key_file" "$decoded_key_file"
}
trap cleanup EXIT HUP INT TERM

cp --preserve=mode,timestamps -- "$env_file" "$backup_file"
chmod 600 "$backup_file"
cp -- "$env_file" "$work_file"
chmod 600 "$work_file"

key_id="$(sed -n 's/^MATCH_ADMISSION_SIGNING_KEY_ID=//p' "$work_file" | tail -n 1)"
private_key="$(sed -n 's/^MATCH_ADMISSION_PRIVATE_KEY_BASE64=//p' "$work_file" | tail -n 1)"
if [[ -z "$key_id" ]]; then
  key_id="match-admission-signing-key-$(date -u +%Y%m)"
fi
if [[ -z "$private_key" ]]; then
  openssl genpkey -algorithm ED25519 -out "$private_key_file" >/dev/null 2>&1
  private_key="$(openssl pkey -in "$private_key_file" -outform DER 2>/dev/null | tail -c 32 | openssl base64 -A)"
fi

set_value() {
  local key="$1"
  local value="$2"
  awk -v prefix="${key}=" 'index($0, prefix) != 1 { print }' "$work_file" >"$next_file"
  printf '%s=%s\n' "$key" "$value" >>"$next_file"
  chmod 600 "$next_file"
  mv -f -- "$next_file" "$work_file"
}

set_value MATCH_ADMISSION_SIGNING_KEY_ID "$key_id"
set_value MATCH_ADMISSION_PRIVATE_KEY_BASE64 "$private_key"
set_value STRICT_ROSTER_LOCKED_GAME_SHA256 "$locked_game_sha256"
set_value STRICT_ROSTER_V1_ENABLED "$enabled"

for key in MATCH_ADMISSION_SIGNING_KEY_ID MATCH_ADMISSION_PRIVATE_KEY_BASE64 \
  STRICT_ROSTER_LOCKED_GAME_SHA256 STRICT_ROSTER_V1_ENABLED; do
  [[ "$(grep -c "^${key}=" "$work_file")" -eq 1 ]] || {
    echo "Failed to write a unique $key setting." >&2
    exit 1
  }
done
printf '%s' "$private_key" | base64 -d >"$decoded_key_file" 2>/dev/null || {
  echo "Match-admission private key is not valid base64." >&2
  exit 1
}
decoded_bytes="$(wc -c <"$decoded_key_file")"
[[ "$decoded_bytes" -eq 32 || "$decoded_bytes" -eq 64 ]] || {
  echo "Match-admission private key has an invalid length." >&2
  exit 1
}

mv -f -- "$work_file" "$env_file"
chmod 600 "$env_file"
printf 'STRICT_ROSTER_ENV_READY enabled=%s key_id=%s backup=%s\n' \
  "$enabled" "$key_id" "$backup_file"
