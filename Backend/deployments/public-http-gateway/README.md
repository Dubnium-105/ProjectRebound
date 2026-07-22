# Public HTTP gateway (no Cloudflare Zero Trust required)

English | [简体中文](README.zh-CN.md)


When the control plane has no public IPv4 address and Cloudflare Tunnel/Zero Trust is unavailable, an existing public gateway can accept ordinary Cloudflare-proxied HTTP traffic. HAProxy listens on public port `443`, inspects TLS SNI, and routes API HTTPS/WSS separately from Relay mTLS. The API connection then reaches the control plane through an independent FRP QUIC channel. HTTP FRP and mTLS FRP must use separate users, tokens, configuration directories, control ports, and systemd units.

```text
boundary.example.com (orange cloud) -> HAProxy :443 -> TLS :10443
  -> HTTP FRPS 127.0.0.1:18081 -> HTTP FRPC -> control 127.0.0.1:18081

relay.example.com (DNS only) -> HAProxy :443 -> mTLS FRPS 127.0.0.1:9443
  -> mTLS FRPC -> control 127.0.0.1:19090
```

## DNS and Cloudflare

- API hostname creates an Orange Cloud A record pointing to the gateway IPv4.
- Relay mTLS hostname continues to use the gray cloud A record pointing to the same IPv4.
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

Change the control-plane mTLS FRPC proxy to `remotePort = 9443`. Verify the FRP configuration at both ends before allowing HAProxy to take over public port 443. Relay clients continue to connect to `relay.example.com:443` and require no change.

Replace `PUBLIC_API_HOST` and `RELAY_MTLS_HOST` in `haproxy.cfg.example` with the actual hostnames. Install the Cloudflare address-refresh script, service, and timer:

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
  -d boundary.example.com
```

Configure the certificate hostname in a root-only defaults file, then install the deployment hook:

```bash
printf '%s\n' 'PUBLIC_API_HOST=boundary.example.com' | \
  sudo tee /etc/default/projectrebound-http-gateway >/dev/null
sudo chown root:root /etc/default/projectrebound-http-gateway
sudo chmod 0600 /etc/default/projectrebound-http-gateway
sudo install -o root -g root -m 0755 deploy-haproxy-cert.sh \
  /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
sudo /etc/letsencrypt/renewal-hooks/deploy/projectrebound-haproxy-cert
sudo certbot renew --dry-run --no-random-sleep-on-renew
```

After renewal, the deployment hook combines `fullchain.pem` and `privkey.pem` into an HAProxy PEM file, replaces it atomically, validates the configuration, and hot-reloads HAProxy. Public port 80 redirects to HTTPS except for ACME HTTP-01. If Cloudflare is accidentally changed to Flexible mode, this setup exposes a redirect failure instead of silently forwarding the API to the origin over plaintext HTTP.

## Acceptance

```bash
sudo haproxy -c -f /etc/haproxy/haproxy.cfg
sudo systemctl is-active haproxy frps projectrebound-http-frps
sudo ss -lntup | grep -E ':(80|443|7000|7001) '
sudo ss -lntp | grep -E '127.0.0.1:(9443|10443|18081) '
curl -fsS https://boundary.example.com/health/ready
```

Finally, confirm that every valid Relay remains continuously online, telemetry stays fresh, and FRPC/FRPS show no new reconnects. Then run staged load tests at 10, 25, 50, and 100 virtual users.
