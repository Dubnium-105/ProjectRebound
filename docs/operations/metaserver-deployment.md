# MetaServer deployment

English | [简体中文](metaserver-deployment.zh-CN.md)

This checklist deploys three independent roles: control-plane MetaServer,
public gateway, and Relay nodes. Use CI artifacts in production. Commands assume
the repository is installed at `/opt/projectrebound/current` and FRP/HAProxy are
already available at the versions used by the existing gateway.

Final DNS:

- `meta.dubnium.top`: A record to gateway `107.172.187.202`, Cloudflare orange
  cloud, SSL mode Full (strict);
- `logic.dubnium.top`: A record to the same gateway, DNS-only/gray cloud so a
  normal TLS TCP client can connect directly.

The production GitHub Environment currently uses
`https://meta.project-rebound.space` for deployment verification, while the
repository's MetaTunnel, MetaServer response, gateway, and monitoring defaults
still use `meta.dubnium.top` and `logic.dubnium.top` for client compatibility.
Both HTTPS names must remain healthy during this compatibility period. Do not
change the Logic hostname on only one side: MetaTunnel verifies that exact TLS
server name. A canonical-domain migration must update the client defaults,
Control Plane/MetaServer configuration, OpenAPI examples, gateway SNI rules,
certificates, and monitoring in one release.

Do not expose 6968, 6969, 8000, 8081, 9000, 16968, 16969, or 18082 publicly.

## 1. CI artifacts and release inputs

CI produces:

- `ghcr.io/<owner>/projectrebound-meta-server:sha-<40-char-commit>`;
- Windows `meta-tunnel.exe` artifact;
- image SBOM, vulnerability result, and provenance;
- release metadata containing protocol version `1`, database migration `35`,
  definitions hash `20393e344e14935535c0eac6815ad82ca051f33caf199281ace4d4bb58391c49`,
  and upstream commit `d68e717267abf14e32d4e39618f9b7680ed93046`.

Promote the exact SHA that passed CI. Production deployment uses the
`production-meta-server` GitHub Environment; staging uses
`staging-meta-server`. The `meta-server` target deploys and rolls back this
image without restarting `control-plane`.

## 2. Control-plane host

Prepare the existing `.env`:

```bash
cd /opt/projectrebound/current/Backend
sudo ./scripts/generate-control-plane-env.sh deployments/control-plane/.env
sudoedit deployments/control-plane/.env
```

Set unique high-entropy `META_POSTGRES_PASSWORD` and `META_REDIS_PASSWORD`.
Keep `META_POSTGRES_USER=projectrebound_meta`,
`META_REDIS_USERNAME=projectrebound-meta`, loopback ports 18082/16968, and the
final public hosts. New generated environments already contain
`ACCESS_TOKEN_PUBLIC_KEY_BASE64` and
`ADMIN_ACCESS_TOKEN_PUBLIC_KEY_BASE64`, plus the separate
`GAME_SERVER_CA_CERT_PEM_BASE64` and `GAME_SERVER_CA_KEY_PEM_BASE64` pair used
by the Control Plane. Compose interpolation requires that pair even during a
Meta-only deployment. For an older environment, add the Game Server CA as
described in the [Dedicated Server registration guide](dedicated-server-registration.md),
then derive each public key without printing the private seed:

If another local service already owns 18082, set
`META_SERVER_HTTP_PORT=18083`. Later, change only the Meta FRPC HTTP
`localPort` to 18083; the gateway `remotePort` remains 18082. This keeps the
host-local AdminWeb and MetaServer listeners isolated without changing the
public route.

```bash
printf '%s\n' "$ACCESS_TOKEN_PRIVATE_KEY_BASE64" |
  ./scripts/derive-ed25519-public.sh
printf '%s\n' "$ADMIN_ACCESS_TOKEN_PRIVATE_KEY_BASE64" |
  ./scripts/derive-ed25519-public.sh
```

Store the outputs in the corresponding public-key variables. MetaServer receives
only these verification keys; it must not receive player/admin signing private
keys or the administrator MFA encryption key. Never copy any token key to the
gateway.

Deploy an immutable CI image:

```bash
cd /opt/projectrebound/current/Backend
sudo env \
  DEPLOY_SOURCE=ci \
  META_SERVER_IMAGE=ghcr.io/<owner>/projectrebound-meta-server:sha-<commit> \
  ./scripts/deploy-meta-server.sh
```

The script builds/pulls only MetaServer, waits for current migration 35,
idempotently provisions a restricted PostgreSQL role and `meta:*` Redis ACL
user, and executes `up -d --no-deps meta-server`. Verify:

`META_MATCH_RESERVATION_TTL_SECONDS` defaults to 90. If no player reaches the
assigned Dedicated Server before this deadline, the scheduler fails the
reservation, returns a healthy server to `READY`, and releases the Party.
Production also requires `META_LOGIC_PROXY_PROTOCOL=true`. The trusted
HAProxy/FRP path supplies this header so per-IP limits use the real client
address; never expose the PROXY-enabled Logic listener to an untrusted network.

```bash
curl -fsS http://127.0.0.1:18082/health/ready
curl -fsS http://127.0.0.1:18082/v1/meta/regions
sudo docker compose --env-file deployments/control-plane/.env \
  -f deployments/control-plane/docker-compose.yaml --profile meta ps meta-server
sudo ss -lntp | grep -E '127.0.0.1:(18082|16968)'
```

Install the isolated FRPC identity. It must not edit or reuse another service's
FRPC config/unit:

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin projectrebound-meta-frpc || true
sudo install -d -o root -g projectrebound-meta-frpc -m 0750 /etc/projectrebound-meta-frpc
sudo install -o root -g projectrebound-meta-frpc -m 0640 \
  deployments/public-http-gateway/frpc-meta.toml.example \
  /etc/projectrebound-meta-frpc/frpc.toml
sudo install -o root -g root -m 0644 \
  deployments/public-http-gateway/projectrebound-meta-frpc.service \
  /etc/systemd/system/
```

Replace only `GATEWAY_IPV4`, then transfer the Meta FRP token through the
existing secure operator channel to `/etc/projectrebound-meta-frpc/token`
owned `root:projectrebound-meta-frpc` mode 0640. Enable the isolated unit:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-meta-frpc
```

## 3. Public gateway

Create a third FRP service, separate from existing HTTP and Relay mTLS services:

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin projectrebound-meta-frps || true
sudo install -d -o root -g projectrebound-meta-frps -m 0750 /etc/projectrebound-meta-frps
openssl rand -hex 32 | sudo tee /etc/projectrebound-meta-frps/token >/dev/null
sudo chown root:projectrebound-meta-frps /etc/projectrebound-meta-frps/token
sudo chmod 0640 /etc/projectrebound-meta-frps/token
sudo install -o root -g projectrebound-meta-frps -m 0640 \
  Backend/deployments/public-http-gateway/frps-meta.toml.example \
  /etc/projectrebound-meta-frps/frps.toml
sudo install -o root -g root -m 0644 \
  Backend/deployments/public-http-gateway/projectrebound-meta-frps.service \
  /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-meta-frps
```

FRPS control port 7002 permits only loopback remote ports 18082 and 16969.
Firewall 7002 to the control-plane egress source only. Do not open those proxy
ports externally.

Obtain certificates for both final hosts. `meta.dubnium.top` must accept
Cloudflare origin traffic; `logic.dubnium.top` must present a publicly trusted
certificate to MetaTunnel:

```bash
sudo certbot certonly --standalone --preferred-challenges http \
  --http-01-address 127.0.0.1 --http-01-port 18888 \
  -d meta.dubnium.top
sudo certbot certonly --standalone --preferred-challenges http \
  --http-01-address 127.0.0.1 --http-01-port 18888 \
  -d logic.dubnium.top
```

Set the root-only renewal defaults and run the supplied atomic HAProxy hook:

```bash
printf '%s\n' \
  'META_HTTP_HOST=meta.dubnium.top' \
  'META_LOGIC_HOST=logic.dubnium.top' | \
  sudo tee -a /etc/default/projectrebound-http-gateway >/dev/null
sudo chmod 0600 /etc/default/projectrebound-http-gateway
sudo install -o root -g root -m 0755 \
  Backend/deployments/public-http-gateway/deploy-haproxy-cert.sh \
  /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
sudo /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
```

Merge the Meta SNI sections from `haproxy.cfg.example` into the deployed
HAProxy configuration. On public 443:

- `meta.dubnium.top` accepts Cloudflare source ranges only, terminates TLS, and
  forwards HTTP to `127.0.0.1:18082`;
- `logic.dubnium.top` terminates normal TLS and forwards the byte stream to
  `127.0.0.1:16969`;
- the Meta Logic TLS listener uses private port `10446`; `10444` remains
  reserved for the existing Admin HTTPS listener;
- both HAProxy Logic hops preserve the client address with PROXY protocol v1,
  which FRP forwards unchanged to the control-plane listener;
- all existing Boundary, Admin, and Relay SNI routes remain unchanged.

Validate before reload:

```bash
sudo haproxy -c -f /etc/haproxy/haproxy.cfg
sudo systemctl reload haproxy
sudo systemctl is-active haproxy projectrebound-meta-frps
sudo ss -lntp | grep -E ':(443|7002)|127.0.0.1:(18082|16969)'
```

## 4. Relay nodes

No Meta-specific domain or manual node list is required. Upgrade each existing
`edge-relay` from its immutable CI image and keep its current Registry identity.
The existing UDP listener recognizes QoS without affecting authenticated Relay
traffic. Defaults are:

```yaml
qos_enabled: true
qos_packets_per_second: 32
qos_max_request_bytes: 256
```

Equivalent environment variables are
`EDGE_RELAY_QOS_ENABLED`, `EDGE_RELAY_QOS_PACKETS_PER_SECOND`, and
`EDGE_RELAY_QOS_MAX_REQUEST_BYTES`. The request must be at least 11 bytes and
begin with `0x59`; malformed packets are silently dropped, and a response never
exceeds the request. Keep the existing public UDP Relay port; do not open 8000
or a second QoS port.

Roll nodes one at a time: drain new allocations, wait for active allocations to
reach zero, deploy, confirm fresh heartbeat/READY, then continue. Never schedule
hourly Relay restarts.

## 5. Client integration

Distribute only the CI-built Windows `meta-tunnel.exe`. Browser passes the
Access Token over anonymous stdin, reads the readiness JSON, and terminates the
tunnel when the game exits. Firewall prompts should not appear because
listeners bind only to random `127.0.0.1` ports. Reject release if certificate
verification or MetaTunnel cannot be enabled.

## 6. Acceptance and rollback

Verify gateway routing without changing DNS:

```bash
curl --resolve meta.dubnium.top:443:107.172.187.202 \
  -fsS https://meta.dubnium.top/health/ready
openssl s_client -connect 107.172.187.202:443 \
  -servername logic.dubnium.top -verify_hostname logic.dubnium.top \
  -verify_return_error </dev/null
```

Then verify authenticated session, Gate single consumption/replay rejection,
profile/loadout round trip and revision conflict, Party, solo/Party matching,
Dedicated Server scope/IDOR, HGH/LAX/gateway Relay dynamic discovery and QoS,
metrics/alerts, and a small canary before the soak test.

The monitoring profile runs a hardened Blackbox Exporter. Confirm that
`probe_success{job="project-rebound-meta-public"}` and
`probe_success{job="project-rebound-logic-public"}` are both 1. These probes
exercise public TLS, HAProxy routing, and the isolated FRP paths rather than
only checking the local container.

On failure, use the deployment workflow's MetaServer rollback or redeploy the
previous immutable MetaServer digest. Do not restart control-plane, do not
rollback migrations 25–35, and do not restore PostgreSQL for an ordinary image
rollback. Gateway/FRP rollback is a separate configuration change validated
with `haproxy -c` and FRP config checks.
