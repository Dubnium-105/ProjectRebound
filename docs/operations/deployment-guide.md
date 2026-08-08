# ProjectRebound Control Plane and Edge Node Separation Deployment Manual

English | [简体中文](deployment-guide.zh-CN.md)

This document corresponds to `Backend/cmd/control-plane` and `Backend/cmd/edge-relay`.

## 1. Deployment topology

```text
客户端 / Dedicated Server --HTTPS/WSS--> Cloudflare Tunnel --> Control Plane
                                                            Caddy / Go / DB
                                                                    ^
                                                                    |
Edge Relay A/B --TLS 1.3 mTLS gRPC--> relay.example.com:443（灰云 DNS）
                                                                    |
                                                               FRPS 公网网关
                                                                    |
                                                          FRPC 独立实例（控制面）
                                                                    |
                                                               127.0.0.1:9090
```

The control plane and edge nodes must be located in different Compose projects and can run on different hosts and different computer rooms. Edge nodes actively connect to the control plane and do not connect to PostgreSQL, Redis or Grafana. When there is no public IPv4 on the control plane, Cloudflare Tunnel continues to be responsible for the HTTP API; the independent FRP gateway only forwards TCP bytes and does not terminate mTLS.

## 2. Host and port

The actual measured 1 vCPU/1.9 GiB host can complete all functional tests, but the HTTP P95 of the 100 VU same-machine stress test is 941.62 ms, which cannot meet the 200 ms acceptance line. The official control plane recommends starting with 4 vCPU, 4 GiB memory, SSD, and running k6 on a standalone host.

### 2.1 Cloudflare Tunnel transmission tuning

Use a package manager to install and keep `cloudflared` updated. Use at least `2026.5.2` to enable the startup process to automatically check DNS, UDP/QUIC 7844, TCP/HTTP2 7844 and Cloudflare API; do not continue the stress test if the check results fail. View the version and most recent preflight:

```bash
cloudflared --version
sudo journalctl -u cloudflared -b --no-pager |
  grep -Ei 'CONNECTIVITY PRE-CHECKS|precheck|Registered tunnel connection'
```

Cloudflare generally recommends `--protocol auto`, which prefers QUIC and falls back to HTTP/2 when UDP is unavailable. A successful connection alone does not prove that link quality is stable: if logs repeatedly show `no recent network activity`, QUIC stream timeouts, or fewer HA connections, pin the protocol to HTTP/2 with `systemctl edit --full cloudflared.service`, then repeat the same-origin A/B test. After production testing, the current control-plane host uses these parameters:

```text
cloudflared --no-autoupdate tunnel --protocol http2 --edge-ip-version 4 run --token <TUNNEL_TOKEN>
```

IPv4 is explicitly used here because the current automatic IPv4/IPv6 routing on the control plane will disperse the connections to LAX/SJC, while IPv4 can stably maintain four LAX HTTP/2 connections. This selection is deployment point dependent and must be retested after migrating the network or computer room and should not be copied directly as the default for all environments.

After modification, verify at least:

```bash
sudo systemctl daemon-reload
sudo systemctl restart cloudflared
sudo systemctl is-active cloudflared
curl -fsS http://127.0.0.1:20241/metrics |
  grep -E '^cloudflared_tunnel_(ha_connections|request_errors|server_locations)'
curl -fsS https://<PUBLIC_API_HOST>/health/ready
```

`cloudflared_tunnel_ha_connections` should be `4` and the request error should remain `0`. Repeat the same k6 scenario from a fixed external load machine, comparing P50, P95, error rate, and RPS; do not use control plane natively generated traffic in lieu of public network acceptance. If the connector can only connect to the remote PoP for a long time, the physical RTT will become a lower limit that cannot be eliminated by the Cloudflare Tunnel parameters. At this time, a closer connector/origin should be added, or an HTTP gateway with a public network address should be used instead.

If the structure of "the public network gateway runs connector and the private network control plane runs origin" is adopted, an independent named tunnel should be created for `boundary.<DOMAIN>` and only this hostname should be published. Do not deploy shared tunnel tokens hosting other domain names directly to the gateway: replicas of the same tunnel do not have fixed traffic steering guarantees, new requests may enter any nearby replica, and the remote ingress configuration will be delivered along with the tunnel. The recommended topology is:

```text
Client -> Cloudflare edge -> gateway cloudflared
       -> gateway loopback-only HTTP origin
       -> isolated QUIC FRP/WireGuard/Tailscale path
       -> control-plane 127.0.0.1:18081
```

The return-to-origin port must only be bound to the gateway loopback address; the return-to-origin FRPS/FRPC must use independent users, configuration directories, tokens, systemd units, and control ports, and the mTLS FRP instance below must not be reused. First use the temporary hostname to do the same origin A/B, confirm the performance and health check, and then switch to the published application route of `boundary.<DOMAIN>`. After switching, keep at least one observation window of the old connector as a fallback, but do not let the shared tunnel and the dedicated tunnel advertise the same hostname at the same time.

The actual measurement conclusion of the current production network is: the 10 VU/1 minute P95 of the control plane connector is 1.05 s; the LAX gateway connector plus independent QUIC back-to-source is reduced to 531 ms, and the error rate is 0. The gateway solution has improved by about 49%, but has still not reached the 200 ms acceptance line, so closer origin/connector or control plane migration is still the final performance improvement item. Quick Tunnel is only used for A/B and cannot be used as a production entrance.

If Cloudflare Zero Trust cannot be activated due to account or payment method, you can use ordinary Orange Cloud HTTP proxy plus self-built SNI gateway, no Tunnel is required. The public network gateway is taken over by HAProxy 443: `boundary.<DOMAIN>` terminates HTTPS and returns to the origin through independent FRP QUIC. Relay mTLS hostname is transparently transmitted to the loopback FRPS with the original TLS. API origin only allows Cloudflare official address range, mTLS domain names remain gray and legal Relay direct connections are allowed. Cloudflare SSL mode must be Full (strict) and Flexible cannot be used permanently. See `Backend/deployments/public-http-gateway/README.md` for the complete configuration and certificate renewal process.

Control plane inbound rules:

|port|source|use|
| --- | --- | --- |
| TCP 22 | Operations network | SSH |
| TCP 80/443 |Public network or Cloudflare Tunnel|Caddy HTTP/HTTPS, WebSocket, Relay registration|
| UDP 443 |Public network|Caddy HTTP/3, optional|
| TCP 9090 |`127.0.0.1` only|FRPC to Relay TLS 1.3 mTLS gRPC control flow|
| TCP 18080 |`127.0.0.1` only|Management API, direct health checks, metrics|
| TCP 5432/6379/9091/3000 | `127.0.0.1` only | PostgreSQL, Redis, Prometheus, Grafana |

Edge node rules:

|direction|port|use|
| --- | --- | --- |
|Inbound UDP|8443, or the configured game relay port|Relay data plane|
| Inbound TCP | 22, operations network only | SSH |
|Outbound TCP|Control plane 443|First time registration, certificate renewal|
|Outbound TCP|mTLS Gateway 443|mTLS gRPC control flow|
|Native TCP| 127.0.0.1:9100 |Relay Prometheus Metrics|

The public network mTLS gateway additionally opens TCP 443 to edge nodes, and limits the FRPS control port TCP/UDP 7000 to only the control plane egress address or the two-machine VPN address. mTLS domains must use Cloudflare DNS Only; Orange Cloud Proxy does not support this arbitrary TCP mTLS channel. See `Backend/deployments/public-mtls-gateway/README.md` for complete configuration, systemd isolation and acceptance commands.

## 3. Debian Preparation

The control plane and each edge node execute:

```bash
sudo apt-get update
sudo apt-get install -y ca-certificates curl git jq openssl docker.io docker-compose
sudo systemctl enable --now docker
sudo docker version
sudo docker compose version
```

If the installation is interrupted abnormally, first confirm that the stuck PID indeed belongs to apt/dpkg, and then execute the following steps:

```bash
ps -ef | grep -E 'apt|dpkg'
sudo dpkg --configure -a
sudo apt-get -f install
sudo dpkg --audit
```

Processes must not be killed in bulk without checking the PID.

When the network cannot access Docker Hub, you can optionally configure a trusted image proxy. The mirror proxy can see the requested mirror name and source IP. The production environment should use a self-built cache; if a third-party proxy is used in the test environment, the privacy boundary must be accepted first. For example:

```json
{
  "registry-mirrors": ["https://docker.m.daocloud.io"]
}
```

Save to `/etc/docker/daemon.json` and then execute `sudo systemctl restart docker`. The official default values ​​for Go module download are `https://proxy.golang.org,direct` and `sum.golang.org`; when the official endpoint is unreachable, the verified alternative value can be enabled in `.env`:

```text
GOPROXY=https://goproxy.cn,direct
GOSUMDB=sum.golang.org https://sum.golang.google.cn
```

## 4. Select publishing source

For production environments, use the immutable GHCR image produced by GitHub Actions. CI publishes `sha-<40-character-commit>` images for the Control Plane, MetaServer, and Edge Relay. The Deploy workflow transfers only a small release bundle containing Compose, verification, and rollback scripts, then pulls the selected image. The target machine does not need Go, a build cache, or a permanent clone of the complete Git repository.

The deployment entry points support `DEPLOY_SOURCE`:

- `ci`: requires the applicable `CONTROL_PLANE_IMAGE`, `META_SERVER_IMAGE`, or `EDGE_RELAY_IMAGE` to be `ghcr.io/...:sha-<40-character-commit>` and only pulls the CI image;
- `source`: Execute Docker Compose/BuildKit native build using the currently checked out source code;
- `auto` (default): Use `ci` when a valid GHCR SHA image is detected, otherwise use `source`.

AutoCD always sets `DEPLOY_SOURCE=ci` explicitly. Use source mode only for offline development or troubleshooting; manual source mode requires a repository checkout:

```bash
git clone <PROJECT_REPOSITORY_URL> project-rebound
cd project-rebound/Backend
```

Execute `docker login ghcr.io` before manually using the private GHCR image. The deployment account only requires the `read:packages` permission of the target package.

## 5. Deploy control plane

### 5.1 Generate key and environment files

```bash
cd project-rebound/Backend
chmod +x scripts/*.sh deploy/deploy.sh
./scripts/generate-control-plane-env.sh
chmod 600 deployments/control-plane/.env
```

The generator creates independent Ed25519 Access Tokens, Relay Tokens, update signing keys, a device-fingerprint HMAC key, a 32-byte VNT room-secret encryption key, separate MinIO root/application credentials, and separate ten-year Relay and Game Server CAs. It does not overwrite the existing `.env`, nor does it output key text. Preserve `GAME_SERVER_CA_*` and MinIO credentials across rebuilds; replacing them prevents existing Dedicated Server certificate renewal or removes management access to stored downloads.

Before deploying a release that supports certificate-backed Dedicated Servers, check an existing environment without printing either secret:

```bash
env_file=deployments/control-plane/.env
test "$(grep -Ec '^GAME_SERVER_CA_(CERT|KEY)_PEM_BASE64=[A-Za-z0-9+/=]+$' "$env_file")" -eq 2
```

If the check fails, generate a separate Game Server CA through the approved secret ceremony and add both values before deployment. Do not replace the complete environment file or reuse the Relay CA. Production Compose rejects a missing pair before changing the running release. The legacy `GAME_SERVER_REGISTRATION_TOKENS` variable is no longer read; remove it from old environments after confirming that all nodes use database-backed, instance-bound Registration Tokens.

Before introducing VNT support into an existing production environment, verify the new key without printing it:

```bash
env_file=deployments/control-plane/.env
vnt_key="$(sed -n 's/^VNT_SECRET_ENCRYPTION_KEY_BASE64=//p' "$env_file")"
test "$(printf '%s' "$vnt_key" | base64 -d | wc -c)" -eq 32
unset vnt_key
```

If it is missing, first make a permission-`600` backup of the existing environment file, generate exactly 32 random bytes through the approved secret manager, store their standard Base64 representation as `VNT_SECRET_ENCRYPTION_KEY_BASE64`, and rerun the check. Production Compose refuses a missing value, and the application refuses a missing or malformed value before serving traffic. Keep this key stable and back it up separately: it encrypts the per-room VNT network token, E2E password, and idempotent host-token recovery material. Losing every copy of a referenced key makes stored VNT room secrets unreadable.

VNT secret-key rotation uses an explicit keyring. Set a new unique `VNT_SECRET_ENCRYPTION_KEY_ID`, replace `VNT_SECRET_ENCRYPTION_KEY_BASE64` with the new 32-byte key, and add the previous `key_id=base64_key` to the semicolon-separated `VNT_SECRET_DECRYPTION_KEYS`. Restart the Control Plane and verify old idempotent room creation and VNT bootstrap before removing any historical key. New/rebound rooms use only the active key ID; old keys remain read-only until no `p2p_rooms.host_token_key_id` or `p2p_vnt_sessions.secret_key_id` references them. Duplicate/invalid key IDs and malformed keys fail startup.

Edit `deployments/control-plane/.env`:

- Change `CORS_ALLOWED_ORIGINS` to the real client source; separate multiple sources with commas.
- Change `UPDATE_CDN_BASE_URL`, `UPDATE_REALTIME_URL`, `UPDATE_STUN_SERVERS` to real addresses.
- Test/IP mode reserved for `PUBLIC_API_SITE=http://:80` and `PUBLIC_API_HTTP_PORT=8080`.
- Domain name production mode settings `PUBLIC_API_SITE=api.example.com`, `PUBLIC_API_HTTP_PORT=80`; DNS A/AAAA points to the control plane and opens 80/443, Caddy automatically applies for a certificate.
- Download management uses same-host MinIO by default. Replace the example hosts in `MINIO_S3_SITE`, `DOWNLOADS_SITE`, `DOWNLOAD_S3_ENDPOINT`, `DOWNLOAD_PUBLIC_BASE_URL`, and `MINIO_CORS_ALLOWED_ORIGINS` together; point both S3 and download DNS A/AAAA records at the Control Plane. The public base must include the bucket name, and the MinIO Console must remain reachable only through an SSH tunnel to `127.0.0.1:MINIO_CONSOLE_PORT`.
- When FRPC and the control plane are deployed on the same machine, `RELAY_CONTROL_BIND_IP` must remain `127.0.0.1`; only when FRPC is located on another trusted private network/VPN host, it is changed to the corresponding private network address and should not be directly bound to `0.0.0.0`.
- `RELAY_CONTROL_SERVER_NAMES` must contain `control_server_name` used by the edge node, for example `control-plane,localhost,relay.example.com`.
- Signing key IDs must be updated during rotation and old IDs cannot be reused after key changes.
- Keep `DEVICE_FINGERPRINT_HMAC_KEY_BASE64` stable and backed up separately. Production refuses to start without it. Raw hardware factors are not stored, so existing device digests cannot be recomputed if this key is lost. Do not change it or `DEVICE_FINGERPRINT_KEY_ID` until a multi-key migration procedure is available.
- Keep the active `VNT_SECRET_ENCRYPTION_KEY_BASE64` and all `VNT_SECRET_DECRYPTION_KEYS` in the same protected backup policy. Development may create an ephemeral key when the active variable is empty, but production never does; an ephemeral key invalidates stored VNT room secrets after restart and is unsuitable for shared staging. Never reuse a `VNT_SECRET_ENCRYPTION_KEY_ID` for different key bytes.
- `STEAM_APP_ID` and the ticket-age settings remain accepted only for configuration and test-fixture compatibility; they do not gate real ticket acceptance. The image contains the standalone Go `/usr/local/bin/decrypt-ticket` verifier, while the official Steamworks Linux `libsdkencryptedappticket.so` and the title's 32-byte encrypted-ticket key must be supplied separately through `STEAM_ENCRYPTED_APP_TICKET_LIBRARY_HOST_PATH` and `STEAM_ENCRYPTED_APP_TICKET_KEY_HOST_PATH`. Both are mounted read-only and are never included in the image. The key file may contain exactly 32 raw bytes or 64 hexadecimal characters. Keep it owned by host root, assign its group to the container `app` GID (pinned to `999`), and use mode `0440`; keep the containing host directory owned by root with mode `0700`. A root-owned `0600` key is not readable by the non-root container process. Before replacing the running container, the deployment script executes an invalid-ciphertext verifier probe as `app` and refuses the release unless the key and native library load successfully. The verifier receives the ticket only on stdin and emits bounded JSON on stdout; the control plane does not contain or fall back to an in-process Steam decryption algorithm.
- Put the canonical ToolBox certificate at `TOOLBOX_PUBKEY_HOST_PATH` and mount it read-only at `TOOLBOX_PUBKEY_PATH`. The exact PEM bytes, including line endings, are hashed into every integrity proof; do not reformat or base64-transform the file. Production refuses to start without this setting. `INTEGRITY_CHALLENGE_TTL_SECONDS` defaults to 120 and `INTEGRITY_MAXIMUM_FAILURES` must remain 3 unless the client and incident-response policy are updated together.

`.env` must be kept in the host secret storage, the permission must be `600`, and it must not be submitted to Git, copied into a mirror, or written into a work order.
Include the `minio-data` volume in a separate off-host backup plan. It is not a substitute for PostgreSQL backups and must not exist only as a single-host, single-disk copy.

### 5.2 Update descriptor

Put the non-secret release descriptor into `Backend/deployments/updates` as per `Backend/deployments/updates/README.md`. Production mode refuses to launch without a valid release descriptor or security update URL. Large files must be placed on object storage/CDN, and the API only returns download metadata.

### 5.3 Startup and Verification

```bash
DEPLOY_SOURCE=ci \
CONTROL_PLANE_IMAGE=ghcr.io/<owner>/projectrebound-control-plane:sha-<40-char-commit> \
  ./scripts/deploy-control-plane.sh
./scripts/verify-control-plane.sh
```

The deployment script will:

1. Reject environment files containing `CHANGE_ME` or `example.com`;
2. Force `.env` permission to `600`;
3. Verify Compose;
4. Pull the CI control plane image and start PostgreSQL, Redis, control plane, Caddy, Prometheus, and Grafana;
5. Apply the current database migrations and wait for `/health/ready`;
6. Output a restricted tail log and return a non-zero status on failure.

When the native monitoring stack is not required:

```bash
ENABLE_MONITORING=0 DEPLOY_SOURCE=ci \
CONTROL_PLANE_IMAGE=ghcr.io/<owner>/projectrebound-control-plane:sha-<40-char-commit> \
  ./scripts/deploy-control-plane.sh
```

Only run `DEPLOY_SOURCE=source ./scripts/deploy-control-plane.sh` if you need to build from the currently checked out source code.

View status:

```bash
sudo docker compose --env-file deployments/control-plane/.env \
  -f deployments/control-plane/docker-compose.yaml --profile monitoring ps
curl -fsS http://127.0.0.1:18080/health/ready
```

For a release that adds VNT support, keep `VNT_ROOMS_ENABLED=false` until migrations through `000038_vnt_security_audit.sql` have completed, `player_feature_grants`, `vnt_nodes`, `p2p_vnt_sessions`, and `vnt_security_audit_logs` exist, at least one compatible node is healthy, and the ToolBox runtime gate has passed. Set the exact, case-sensitive `VNT_ALLOWED_VNTS_VERSIONS` and `VNT_ALLOWED_WRAPPER_VERSIONS` CSV allowlists first; startup validation rejects an enabled VNT feature with either list empty or malformed, and room creation/rebind rejects nodes outside either list. `VNT_CREDENTIAL_ROTATION_GRACE_SECONDS` controls the old Node Token's heartbeat-only rotation overlap, defaults to 60 seconds, and accepts `1..600`; it does not extend the old token's management authority. `VNT_MAX_NODES_PER_PLAYER` defaults to three non-`RETIRED` nodes and accepts `1..100`. Independent limiter defaults are five enrollments per player per hour, 120 directory reads per source IP per minute, 30 room bootstraps per player per minute, 120 heartbeats per credential per minute, and ten rotate/retire operations per credential per hour; tune them with `VNT_ENROLLMENT_REQUESTS_PER_PLAYER_PER_HOUR`, `VNT_DIRECTORY_REQUESTS_PER_IP_PER_MINUTE`, `VNT_BOOTSTRAP_REQUESTS_PER_PLAYER_PER_MINUTE`, `VNT_HEARTBEAT_REQUESTS_PER_CREDENTIAL_PER_MINUTE`, and `VNT_MANAGEMENT_REQUESTS_PER_CREDENTIAL_PER_HOUR`. Do not infer migration success from HTTP liveness alone; `/health/ready` and the migration/table check must both pass. Set `VNT_ROOMS_ENABLED=true`, recreate only the Control Plane container, and verify `/v1/client/config` returns `features.vnt_rooms=true` before opening client traffic. Turning it back to `false` blocks new VNT room creation/rebind while allowing existing sessions to drain.

Before enabling VNT, also verify `/internal/metrics` reports `vnt_nodes_compatible_online >= 1`, `vnt_node_credentials_expired == 0`, and no active `NoCompatibleVNTNodeAvailable` or `VNTNodeCredentialExpired` alert. Treat `VNTNodeCredentialExpiringSoon` as a rotation work item rather than waiting for runtime authentication to fail.

Access monitoring from the operations workstation:

```bash
ssh -L 9091:127.0.0.1:9091 -L 3000:127.0.0.1:3000 user@CONTROL_HOST
```

## 6. Deploy the first edge node

### 6.1 Prepare one-time credentials on the control plane

The format of `RELAY_BOOTSTRAP_TOKENS` is `credential_id=token`, with multiple credentials separated by semicolons. The generator has created the first item. Pass the token value on the right side of the equal sign to the corresponding edge node through the secret manager. Do not display it in chat, logs or command output.

Each new edge node uses a different credential ID and a different random token. Once the old token is registered, it will be marked as consumed in the database and cannot be reused.

### 6.2 Configure edge nodes

On the edge host:

```bash
cd project-rebound/Backend
cp deployments/edge-relay/.env.example deployments/edge-relay/.env
cp deployments/edge-relay/config.edge-relay.yaml.example \
   deployments/edge-relay/config.edge-relay.yaml
chmod 600 deployments/edge-relay/.env
```

Edit `.env` and set `EDGE_RELAY_BOOTSTRAP_TOKEN` only on first registration. Edit YAML:

- `control_plane_url`: Public network HTTPS API, such as `https://api.example.com`.
- `control_addr`: Stable mTLS gateway address, such as `relay.example.com:443`; when using private network/VPN direct connection, you can fill in `10.20.0.10:9090`.
- `control_server_name`: must be consistent with the certificate domain name and control plane `RELAY_CONTROL_SERVER_NAMES` used by `control_addr`, for example, `relay.example.com`, and should not be changed to IP.
- `advertised_endpoints[].host`: The public IP or domain name actually reachable by the client.
- `advertised_endpoints[].port`: UDP port after public network mapping.
- `region`, `zone`, `provider`, capacity and bandwidth: fill in the real information of the node.

Detached edge Compose uses Linux host networking, so a Prometheus agent on the host can scrape `127.0.0.1:9100` without exposing it publicly. Regular monitoring does not require a separate scrape path for every new node: each Relay reuses the mTLS control channel to report cumulative telemetry, and `/internal/metrics` on the control plane exposes all registered nodes uniformly. Scraping the node-local port 9100 is an optional troubleshooting enhancement.

### 6.3 First startup

```bash
chmod +x scripts/deploy-edge-relay.sh
DEPLOY_SOURCE=ci \
EDGE_RELAY_IMAGE=ghcr.io/<owner>/projectrebound-edge-relay:sha-<40-char-commit> \
  ./scripts/deploy-edge-relay.sh
```

The script waits for `relay control connected`, then automatically clears the one-time token in the edge `.env`, and forces the container to be rebuilt to connect again. This step also verifies that `/edge-relay-data/identity.json` has been persisted. Do not delete the `project-rebound-edge-relay_edge-relay-data` volume, otherwise a new Bootstrap Token must be issued and re-registered.

Confirm listening and local indicators:

```bash
sudo ss -lunp | grep ':8443'
curl -fsS http://127.0.0.1:9100/metrics
sudo docker compose --env-file deployments/edge-relay/.env \
  -f deployments/edge-relay/docker-compose.yaml logs --tail=50 edge-relay
```

Query the node through the loopback management port on the control plane:

```bash
curl -fsS 'http://127.0.0.1:18080/internal/v1/relay-nodes?limit=100' \
  -H 'Authorization: Bearer ADMIN_TOKEN'
curl -fsS http://127.0.0.1:18080/internal/v1/relay-nodes/RELAY_NODE_ID \
  -H 'Authorization: Bearer ADMIN_TOKEN'
```

The desired status is `READY`.

## 7. Add, offline and restore edge nodes

When adding a node, generate a new high-entropy token, append the new `id=token` to the control plane `RELAY_BOOTSTRAP_TOKENS`, redeploy the control plane, and then deploy edge nodes according to Section 6.

Planned maintenance:

```bash
curl -X POST http://127.0.0.1:18080/internal/v1/relay-nodes/NODE_ID/drain \
  -H 'Authorization: Bearer ADMIN_TOKEN'
```

Stop the edge container after confirming that the existing allocation is drained. Call `/resume` after recovery. `/revoke` is called when certificate or node credentials are leaked. This operation is irreversible; the revoked node must re-register with a new identity.

Control plane rebuilds, upgrades, or reboots must not replace `RELAY_CA_*`. Edge nodes automatically reconnect as long as the CA and edge identity volumes remain unchanged. Relay CA rotation requires a dual CA/dual certificate migration solution, and the current version cannot complete non-disruptive rotation through direct replacement.

## 8. Backup, recovery and upgrade

Create and verify a PostgreSQL custom-format backup:

```bash
./scripts/backup-control-plane.sh /srv/project-rebound-backups
```

The backup directory permission is `700` and the backup file is `600`. Encrypted replication of backups to another host/region and periodic restores to quarantine database verification. Production recovery steps: Stop control plane writes, keep a backup of the current database, restore to an explicit database using `pg_restore --single-transaction --clean --if-exists`, rerun the migration, and then perform a smoke test. Recovery is a destructive operation and is not performed by automated deployment scripts.

The upgrade is recommended to be done through the Deploy workflow of GitHub Actions: select the target environment and node, fill in the complete commit SHA that has passed CI and still exists in GHCR. The workflow will first back up the control plane database, then pull the image of the same SHA, perform a health check, and restore the previous release in case of failure.

When the control plane must be upgraded manually on the host:

```bash
./scripts/backup-control-plane.sh /srv/project-rebound-backups
DEPLOY_SOURCE=ci \
CONTROL_PLANE_IMAGE=ghcr.io/<owner>/projectrebound-control-plane:sha-<40-char-commit> \
  ./scripts/deploy-control-plane.sh
./scripts/verify-control-plane.sh
```

Edge nodes can be drained one by one and run Deploy using the same CI commit SHA, or run `./scripts/deploy-edge-relay.sh` as `DEPLOY_SOURCE=ci` as per Section 6.3. Applying rollback deploys the last verified SHA that still exists in GHCR; rollback the database only if the migration is not backwards compatible and a recovery plan is in place.

## 9. Complete acceptance

Before publishing, do at least:

```bash
cd Backend
go vet ./...
go test ./... -count=1
./scripts/verify-control-plane.sh
```

Then execute:

- Real PostgreSQL integration testing and migration testing;
- Auth bind/refresh/logout, ban permissions;
- Dedicated Server registration/heartbeat/logout;
- P2P create/join/leave/start;
- WebSocket candidate exchange and Relay fallback;
- Relay drain/resume, reconnect after the control plane is rebuilt;
- Public DNS gray cloud resolution, no client certificate rejection, valid relay certificate forward mTLS handshake and server certificate automatic rotation;
- Update Manifest signature and file SHA-256;
- Backup and restore;
- `tests/netem/run-relay-matrix.sh` in the independent network namespace;
- `tests/load/control-plane.js` on a standalone loader.

Performance acceptance requirements HTTP P95 `< 200 ms`, HTTP failure rate `< 1%`, check success rate `> 99%`, WebSocket upgrade P95 `< 1 s`. Functional success does not equate to passing performance thresholds, and results must be reported separately.

## 10. Security invariants

- PostgreSQL, Redis, Grafana, Prometheus, direct control plane HTTP and Relay mTLS backend ports can only be bound to loopback addresses.
- FRPS only allows remote proxy TCP 443; control port 7000 is not open to any public network source, and FRPC must be isolated from existing panel/application FRPC.
- Public network Caddy returns 404 for `/v1/admin*` and `/internal/*`, and only allows Relay registration and certificate renewal.
- Admin Token is not interchangeable with player Token, Game Server Token, and Relay Token.
- Access, Refresh, Admin, Bootstrap, Relay, Game Server Token and private keys are not allowed to enter the log.
- The edge node does not own the database or Redis credentials and does not parse the game payload.
- Do not run netem on production physical NICs; only use isolated namespace/veth.
- Do not submit `.env`, `identity.json`, database backup or signing private key to Git.

See `docs/api/external.md` for the external API, `docs/api/internal.md` for the internal and Relay API, and `Backend/api/openapi/openapi.yaml` for the machine-readable contract.

For methods of building GHCR images, configuring staging/production environment, automatic backup deployment and rollback through GitHub Actions, see `docs/operations/ci-cd.md`.
