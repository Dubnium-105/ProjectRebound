# Public HTTP gateway (no Cloudflare Zero Trust required)

English | [简体中文](README.zh-CN.md)

When the control plane has no public IPv4 address and Cloudflare Tunnel/Zero Trust is unavailable, an existing public gateway can accept ordinary Cloudflare-proxied HTTP traffic. HAProxy listens on public port `443`, inspects TLS SNI, and routes API HTTPS/WSS separately from Relay mTLS. The API connection then reaches the control plane through an independent FRP QUIC channel. HTTP FRP and mTLS FRP must use separate users, tokens, configuration directories, control ports, and systemd units.

```text
project-rebound.space (orange cloud) -> HAProxy :443 -> TLS :10443
  -> HTTP FRPS 127.0.0.1:18081 -> HTTP FRPC -> control 127.0.0.1:18081

relay.project-rebound.space (DNS only) -> HAProxy :443 -> mTLS FRPS 127.0.0.1:9443
  -> mTLS FRPC -> control 127.0.0.1:19090
```

## DNS and Cloudflare

- The production API hostname is `project-rebound.space`; create an Orange Cloud A/AAAA record pointing to the gateway.
- Relay mTLS uses `relay.project-rebound.space` as DNS-only/gray-cloud traffic to the same gateway.
- In Cloudflare, select **Full (strict)** under `SSL/TLS > Overview`; do not use Flexible mode.
- Gateway certificates can use Let's Encrypt. HAProxy forwards the ACME HTTP-01 path to the local Certbot standalone listener at `127.0.0.1:18888`.
- The API's 80/443 origin requests only allow the official Cloudflare address range; Relay mTLS SNI is not restricted by this origin.

## Standalone HTTP FRP

Install `frps.toml.example` and `projectrebound-http-frps.service` under `/etc/projectrebound-http-frps` and `/etc/systemd/system`, respectively. Generate a dedicated token:

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin projectrebound-http-frps
sudo install -d -o root -g projectrebound-http-frps -m 0750 /etc/projectrebound-http-frps
openssl rand -hex 32 | sudo tee /etc/projectrebound-http-frps/token >/dev/null
sudo chown root:projectrebound-http-frps /etc/projectrebound-http-frps/token
sudo chmod 0640 /etc/projectrebound-http-frps/token
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-http-frps
```

Transfer the same token to `/etc/projectrebound-http-frpc/token` on the control plane through a secure channel, then install the FRPC template and unit. The gateway must allow only the control-plane egress IP to reach TCP/UDP 7001. `proxyBindAddr = "127.0.0.1"` forces the remote proxy port to bind only to loopback.

## 443 SNI migration

Existing mTLS FRPS must be changed to:

```toml
proxyBindAddr = "127.0.0.1"
allowPorts = [{ single = 9443 }]
```

Change the control-plane mTLS FRPC proxy to `remotePort = 9443`. Verify the FRP configuration at both ends before allowing HAProxy to take over public port 443. Relay clients connect to `relay.project-rebound.space:443`.

Replace `PUBLIC_API_HOST` and `RELAY_MTLS_HOST` in `haproxy.cfg.example` with the actual hostnames. For production, use `project-rebound.space` and `relay.project-rebound.space`. Install the Cloudflare address-refresh script, service, and timer:

```bash
sudo install -o root -g root -m 0755 refresh-cloudflare-ips.sh /usr/local/sbin/projectrebound-refresh-cloudflare-ips
sudo install -o root -g root -m 0644 projectrebound-cloudflare-ips.service projectrebound-cloudflare-ips.timer /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-cloudflare-ips.timer
sudo systemctl start projectrebound-cloudflare-ips.service
```

## TLS certificate

```bash
sudo certbot certonly --standalone --non-interactive --agree-tos \
  --register-unsafely-without-email --preferred-challenges http \
  --http-01-address 127.0.0.1 --http-01-port 18888 \
  -d project-rebound.space
```

Configure the certificate hostname in a root-only defaults file, then install the deployment hook. When the Admin Web uses a separate hostname, also set `ADMIN_WEB_HOST`; the hook installs both renewed certificate chains before validating and reloading HAProxy:

```bash
printf '%s\n' \
  'PUBLIC_API_HOST=project-rebound.space' \
  'ADMIN_WEB_HOST=admin.project-rebound.space' | \
  sudo tee /etc/default/projectrebound-http-gateway >/dev/null
sudo chown root:root /etc/default/projectrebound-http-gateway
sudo chmod 0600 /etc/default/projectrebound-http-gateway
sudo install -o root -g root -m 0755 deploy-haproxy-cert.sh \
  /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
sudo /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
sudo certbot renew --dry-run --no-random-sleep-on-renew
```

After renewal, the deployment hook combines `fullchain.pem` and `privkey.pem` into an HAProxy PEM file, replaces it atomically, validates the configuration, and hot-reloads HAProxy. Public port 80 redirects to HTTPS except for ACME HTTP-01. If Cloudflare is accidentally changed to Flexible mode, this setup exposes a redirect failure instead of silently forwarding the API to the origin over plaintext HTTP.

## Isolated MetaServer HTTP and Logic routes

The final Meta routes use a third FRP identity and do not modify the existing HTTP or Relay mTLS FRP services:

```text
meta.project-rebound.space (orange cloud) -> HAProxy :443 -> TLS :10445
  -> Meta FRPS 127.0.0.1:18082 -> Meta FRPC -> control 127.0.0.1:18082

logic.project-rebound.space (DNS only) -> HAProxy :443 -> TLS :10446
  -> Meta FRPS 127.0.0.1:16969 -> Meta FRPC -> control 127.0.0.1:16968
```

The two HAProxy Logic hops use PROXY protocol v1 so MetaServer sees the real client address after FRP. Keep 10446, 16969, and the PROXY-enabled control listener private; accepting this header from an untrusted source permits IP spoofing.

Port 10446 is intentionally separate from the deployed Admin HTTPS listener on 10444. Do not reuse the Admin listener or certificate for Meta Logic.

Install `frps-meta.toml.example` and `projectrebound-meta-frps.service` on the gateway under `/etc/projectrebound-meta-frps`. Install `frpc-meta.toml.example` and `projectrebound-meta-frpc.service` on the control host under `/etc/projectrebound-meta-frpc`. Use a unique token and FRP user; firewall control port 7002 to the control-plane source. The gateway terminates both certificates. Meta HTTP accepts Cloudflare source ranges only; Logic must remain reachable by normal TLS clients and is therefore not Cloudflare-source restricted. The Meta HTTPS frontend returns 404 for `/internal/*` and `/v1/admin*`; administrators use the existing isolated Admin origin.

When the FRP control connection uses a two-host VPN such as Tailscale, bind FRPS to the VPN address instead of `0.0.0.0`. Prefer FRP `tcp` transport over that overlay if nested QUIC repeatedly reports `no recent network activity`; FRP TLS and the VPN encryption remain enabled.

Set `META_HTTP_HOST=meta.project-rebound.space` and `META_LOGIC_HOST=logic.project-rebound.space` in the root-only gateway defaults before running the certificate deployment hook. Ports 18082 and 16969 remain loopback-only. The complete ordered procedure is in [`docs/operations/metaserver-deployment.md`](../../../docs/operations/metaserver-deployment.md).

The control-plane local HTTP port may differ from the gateway remote port. For example, when AdminWeb already owns control-plane `127.0.0.1:18082`, set `META_SERVER_HTTP_PORT=18083` and change only the Meta FRPC proxy `localPort` to `18083`; keep its `remotePort` and the gateway origin on `18082`.

## Acceptance

```bash
sudo haproxy -c -f /etc/haproxy/haproxy.cfg
sudo systemctl is-active haproxy frps projectrebound-http-frps projectrebound-meta-frps
sudo ss -lntup | grep -E ':(80|443|7000|7001|7002) '
sudo ss -lntp | grep -E '127.0.0.1:(9443|10443|10444|10445|10446|18081|18082|16969) '
curl -fsS https://meta.project-rebound.space/health/ready
test "$(curl -sS -o /dev/null -w '%{http_code}' https://meta.project-rebound.space/internal/metrics)" = 404
openssl s_client -connect 127.0.0.1:443 -servername logic.project-rebound.space \
  -verify_hostname logic.project-rebound.space -verify_return_error </dev/null
```

Finally, confirm that every valid Relay remains continuously online, telemetry stays fresh, and FRPC/FRPS show no new reconnects. Then run staged load tests at 10, 25, 50, and 100 virtual users.
