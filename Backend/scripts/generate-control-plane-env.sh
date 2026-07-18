#!/usr/bin/env sh
set -eu

script_dir="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
backend_dir="$(CDPATH= cd -- "$script_dir/.." && pwd)"
output="${1:-$backend_dir/deployments/control-plane/.env}"

if [ -e "$output" ]; then
  printf 'Refusing to overwrite existing secret file: %s\n' "$output" >&2
  exit 1
fi
command -v openssl >/dev/null 2>&1 || { printf 'openssl is required\n' >&2; exit 1; }

umask 077
temporary_dir="$(mktemp -d)"
trap 'rm -rf "$temporary_dir"' EXIT HUP INT TERM
openssl genpkey -algorithm ED25519 -out "$temporary_dir/relay-ca.key" >/dev/null 2>&1
openssl req -new -x509 -key "$temporary_dir/relay-ca.key" \
  -out "$temporary_dir/relay-ca.crt" -days 3650 \
  -subj '/CN=Project Rebound Relay CA' \
  -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign,digitalSignature' >/dev/null 2>&1

random_hex() { openssl rand -hex "$1"; }
random_key() { openssl rand -base64 32 | tr -d '\r\n'; }
file_base64() { openssl base64 -A -in "$1"; }
key_suffix="$(date -u +%Y%m)"
relay_bootstrap_id="relay-$(date -u +%Y%m%d)-01"

mkdir -p "$(dirname -- "$output")"
cat >"$output" <<EOF
POSTGRES_DB=projectrebound
POSTGRES_USER=projectrebound
POSTGRES_PASSWORD=$(random_hex 32)
REDIS_PASSWORD=$(random_hex 32)

PUBLIC_API_SITE=http://:80
PUBLIC_API_BIND_IP=0.0.0.0
PUBLIC_API_HTTP_PORT=8080
PUBLIC_API_HTTPS_PORT=443
CONTROL_PLANE_ADMIN_PORT=18080
RELAY_CONTROL_BIND_IP=0.0.0.0
RELAY_CONTROL_PORT=9090

CORS_ALLOWED_ORIGINS=https://game.example.com
ACCESS_TOKEN_KEY_ID=access-signing-key-$key_suffix
ACCESS_TOKEN_PRIVATE_KEY_BASE64=$(random_key)
ADMIN_TOKENS=operator=$(random_hex 48)
GAME_SERVER_REGISTRATION_TOKENS=default=$(random_hex 48)

RELAY_BOOTSTRAP_TOKENS=$relay_bootstrap_id=$(random_hex 48)
RELAY_CA_CERT_PEM_BASE64=$(file_base64 "$temporary_dir/relay-ca.crt")
RELAY_CA_KEY_PEM_BASE64=$(file_base64 "$temporary_dir/relay-ca.key")
RELAY_TOKEN_KEY_ID=relay-signing-key-$key_suffix
RELAY_TOKEN_PRIVATE_KEY_BASE64=$(random_key)

UPDATE_CDN_BASE_URL=https://cdn.example.com/project-rebound
UPDATE_SIGNING_KEY_ID=update-signing-key-$key_suffix
UPDATE_SIGNING_PRIVATE_KEY_BASE64=$(random_key)
UPDATE_DEFAULT_CHANNEL=stable
UPDATE_MINIMUM_CLIENT_VERSION=1.0.0
UPDATE_REALTIME_URL=wss://api.example.com/v1/realtime/connect
UPDATE_STUN_SERVERS=stun:stun.example.com:3478

GRAFANA_ADMIN_USER=admin
GRAFANA_ADMIN_PASSWORD=$(random_hex 32)
LOG_LEVEL=info
EOF
chmod 600 "$output"
printf 'Generated %s with mode 600. Edit public URLs/origins before deployment.\n' "$output"
printf 'Copy only the relay bootstrap token value to the first edge node over a secure channel.\n'
