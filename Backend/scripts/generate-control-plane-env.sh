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
openssl genpkey -algorithm ED25519 -out "$temporary_dir/game-server-ca.key" >/dev/null 2>&1
openssl req -new -x509 -key "$temporary_dir/game-server-ca.key" \
  -out "$temporary_dir/game-server-ca.crt" -days 3650 \
  -subj '/CN=Project Rebound Game Server CA' \
  -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
  -addext 'keyUsage=critical,keyCertSign,cRLSign,digitalSignature' >/dev/null 2>&1
openssl genpkey -algorithm ED25519 -out "$temporary_dir/access-token.key" >/dev/null 2>&1
openssl genpkey -algorithm ED25519 -out "$temporary_dir/admin-access-token.key" >/dev/null 2>&1

random_hex() { openssl rand -hex "$1"; }
random_key() { openssl rand -base64 32 | tr -d '\r\n'; }
file_base64() { openssl base64 -A -in "$1"; }
ed25519_private_seed_base64() {
  openssl pkey -in "$1" -outform DER 2>/dev/null |
    tail -c 32 |
    openssl base64 -A
}
ed25519_public_base64() {
  openssl pkey -in "$1" -pubout -outform DER 2>/dev/null |
    tail -c 32 |
    openssl base64 -A
}
key_suffix="$(date -u +%Y%m)"
relay_bootstrap_id="relay-$(date -u +%Y%m%d)-01"

mkdir -p "$(dirname -- "$output")"
cat >"$output" <<EOF
POSTGRES_DB=projectrebound
POSTGRES_USER=projectrebound
POSTGRES_PASSWORD=$(random_hex 32)
REDIS_PASSWORD=$(random_hex 32)
META_POSTGRES_USER=projectrebound_meta
META_POSTGRES_PASSWORD=$(random_hex 32)
META_REDIS_USERNAME=projectrebound-meta
META_REDIS_PASSWORD=$(random_hex 32)

PUBLIC_API_SITE=http://:80
ADMIN_WEB_SITE=admin.example.com
PUBLIC_API_BIND_IP=0.0.0.0
PUBLIC_API_HTTP_PORT=8080
PUBLIC_API_HTTPS_PORT=443
CONTROL_PLANE_ADMIN_PORT=18080
META_SERVER_HTTP_PORT=18082
META_SERVER_LOGIC_PORT=16968
META_PROTOCOL_VERSION=1
RELAY_CONTROL_BIND_IP=127.0.0.1
RELAY_CONTROL_PORT=9090
RELAY_CONTROL_SERVER_NAMES=control-plane,localhost,relay.example.com

CORS_ALLOWED_ORIGINS=https://game.example.com
ACCESS_TOKEN_KEY_ID=access-signing-key-$key_suffix
ACCESS_TOKEN_PRIVATE_KEY_BASE64=$(ed25519_private_seed_base64 "$temporary_dir/access-token.key")
ACCESS_TOKEN_PUBLIC_KEY_BASE64=$(ed25519_public_base64 "$temporary_dir/access-token.key")
DEVICE_FINGERPRINT_KEY_ID=device-fingerprint-v1
DEVICE_FINGERPRINT_HMAC_KEY_BASE64=$(random_key)
STEAM_APP_ID=480
STEAM_TICKET_VERIFIER_PATH=/usr/local/bin/decrypt-ticket
STEAM_TICKET_VERIFIER_TIMEOUT_SECONDS=3
STEAM_TICKET_MAXIMUM_AGE_SECONDS=300
STEAM_ENCRYPTED_APP_TICKET_LIBRARY_HOST_PATH=/opt/projectrebound/libsdkencryptedappticket.so
STEAM_ENCRYPTED_APP_TICKET_LIBRARY=/usr/local/lib/libsdkencryptedappticket.so
STEAM_ENCRYPTED_APP_TICKET_KEY_HOST_PATH=/opt/projectrebound/steam-encrypted-app-ticket.key
STEAM_ENCRYPTED_APP_TICKET_KEY_FILE=/run/projectrebound/steam-encrypted-app-ticket.key
TOOLBOX_PUBKEY_HOST_PATH=/opt/projectrebound/signer.pem
TOOLBOX_PUBKEY_PATH=/run/projectrebound/toolbox-signer.pem
INTEGRITY_CHALLENGE_TTL_SECONDS=120
INTEGRITY_MAXIMUM_FAILURES=3
ADMIN_TOKENS=operator=$(random_hex 48)
ADMIN_ACCESS_TOKEN_KEY_ID=admin-access-signing-key-$key_suffix
ADMIN_ACCESS_TOKEN_PRIVATE_KEY_BASE64=$(ed25519_private_seed_base64 "$temporary_dir/admin-access-token.key")
ADMIN_ACCESS_TOKEN_PUBLIC_KEY_BASE64=$(ed25519_public_base64 "$temporary_dir/admin-access-token.key")
ADMIN_MFA_ENCRYPTION_KEY_BASE64=$(random_key)
TURNSTILE_SITE_KEY=CHANGE_ME
TURNSTILE_SECRET_KEY=CHANGE_ME
TURNSTILE_EXPECTED_HOSTNAME=admin.example.com
TURNSTILE_EXPECTED_ACTION=admin_login
GAME_SERVER_CA_CERT_PEM_BASE64=$(file_base64 "$temporary_dir/game-server-ca.crt")
GAME_SERVER_CA_KEY_PEM_BASE64=$(file_base64 "$temporary_dir/game-server-ca.key")
VNT_SECRET_ENCRYPTION_KEY_BASE64=$(random_key)
VNT_SECRET_ENCRYPTION_KEY_ID=vnt-room-v1
VNT_SECRET_DECRYPTION_KEYS=
VNT_ROOMS_ENABLED=false
VNT_ALLOWED_VNTS_VERSIONS=
VNT_ALLOWED_WRAPPER_VERSIONS=0.1.0
VNT_CREDENTIAL_ROTATION_GRACE_SECONDS=60
VNT_ENROLLMENT_REQUESTS_PER_PLAYER_PER_HOUR=5
VNT_DIRECTORY_REQUESTS_PER_IP_PER_MINUTE=120
VNT_BOOTSTRAP_REQUESTS_PER_PLAYER_PER_MINUTE=30
VNT_HEARTBEAT_REQUESTS_PER_CREDENTIAL_PER_MINUTE=120
VNT_MANAGEMENT_REQUESTS_PER_CREDENTIAL_PER_HOUR=10
VNT_MAX_NODES_PER_PLAYER=3
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

# Enable only after configuring the S3-compatible bucket, CDN, and bucket CORS.
DOWNLOADS_ENABLED=false
DOWNLOAD_S3_ENDPOINT=https://ACCOUNT_ID.r2.cloudflarestorage.com
DOWNLOAD_S3_REGION=auto
DOWNLOAD_S3_BUCKET=project-rebound-downloads
DOWNLOAD_S3_ACCESS_KEY_ID=REPLACE_WHEN_ENABLED
DOWNLOAD_S3_SECRET_ACCESS_KEY=REPLACE_WHEN_ENABLED
DOWNLOAD_PUBLIC_BASE_URL=https://downloads.example.com
DOWNLOAD_ALLOWED_EXTENSIONS=exe,msi,zip,7z,pdf,md,txt,docx
DOWNLOAD_MAX_FILE_BYTES=2147483648
DOWNLOAD_MULTIPART_THRESHOLD_BYTES=67108864
DOWNLOAD_PART_SIZE_BYTES=16777216
DOWNLOAD_UPLOAD_SESSION_TTL_HOURS=24
DOWNLOAD_PRESIGN_TTL_MINUTES=15
DOWNLOAD_VERIFICATION_INTERVAL_SECONDS=5

GRAFANA_ADMIN_USER=admin
GRAFANA_ADMIN_PASSWORD=$(random_hex 32)
LOG_LEVEL=info
EOF
chmod 600 "$output"
printf 'Generated %s with mode 600. Edit public URLs/origins before deployment.\n' "$output"
printf 'Copy only the relay bootstrap token value to the first edge node over a secure channel.\n'
