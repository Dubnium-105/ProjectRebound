# Public mTLS FRP gateway

English | [简体中文](README.zh-CN.md)


When the control plane has no public IPv4 address and Cloudflare Tunnel cannot proxy raw TCP mTLS/gRPC, run FRPS on a lightweight host with public IPv4. A dedicated FRPC instance on the control plane maps its loopback mTLS port to TCP 443 on the gateway. Cloudflare Tunnel remains responsible only for the control-plane HTTPS/WSS API and does not need to be installed on the FRP gateway.

## DNS and ports

- The A record for `relay.example.com` points to the gateway's public IPv4 and must use DNS Only (grey cloud). Do not create an AAAA record unless public IPv6 is available.
- The gateway public network is open to TCP 443; TCP/UDP 7000 only allows access from the control plane egress address or the two-machine VPN address. UDP 7000 is used for the default QUIC control link, and TCP 7000 is reserved as the fallback path.
- The control plane does not expose the Relay control port to the LAN or public network: `RELAY_CONTROL_BIND_IP=127.0.0.1`.
- FRPS `allowPorts` permits only port 443, and each client may create at most one proxy.
- Use a random FRP token of at least 32 bytes and transfer it to both configurations through a secure channel. Set configuration permissions to `0640` and never commit the token to Git.

## Install gateway FRPS

Download matching pinned versions of `frps` and `frpc` from an official FRP release and verify the release hash before installation. Run these commands on the gateway:

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin frp
sudo install -d -o root -g frp -m 0750 /etc/frp
sudo install -o root -g root -m 0755 frps /usr/local/bin/frps
sudo install -o root -g frp -m 0640 frps.toml /etc/frp/frps.toml
sudo install -o root -g root -m 0644 frps.service /etc/systemd/system/frps.service
sudo systemctl daemon-reload
sudo systemctl enable --now frps
sudo systemctl status frps --no-pager
```

Use `frps.toml.example` as the template and replace only `auth.token`. If the gateway already hosts Xray, Hysteria, Squid, or other services, run `ss -lntup` first to confirm that ports 443 and 7000 are free. Do not modify or restart unrelated services.

## Install the dedicated control-plane FRPC

Do not reuse FRPC containers managed by 1Panel or other applications. Create independent users, directories, configurations and units:

```bash
sudo useradd --system --home /nonexistent --shell /usr/sbin/nologin projectrebound-frpc
sudo install -d -o root -g projectrebound-frpc -m 0750 /etc/projectrebound-frpc
sudo install -o root -g root -m 0755 frpc /usr/local/bin/frpc
sudo install -o root -g projectrebound-frpc -m 0640 frpc.toml /etc/projectrebound-frpc/frpc.toml
sudo install -o root -g root -m 0644 projectrebound-frpc.service /etc/systemd/system/projectrebound-frpc.service
sudo systemctl daemon-reload
sudo systemctl enable --now projectrebound-frpc
sudo systemctl status projectrebound-frpc --no-pager
```

Use `frpc.toml.example` as the template, set the gateway address and matching token, and make `localPort` equal to `RELAY_CONTROL_PORT` in the control-plane `.env`. The control plane must also set:

```text
RELAY_CONTROL_BIND_IP=127.0.0.1
RELAY_CONTROL_SERVER_NAMES=control-plane,localhost,relay.example.com
```

The template uses QUIC by default and pre-establishes two work connections. On a lossy cross-region link, this avoids waiting for a new TCP work connection whenever an edge node reconnects. `tcpMuxKeepaliveInterval` probes the reused session, so the separate FRP heartbeat is disabled. Before deployment, use a temporary foreground FRPC configuration to confirm that QUIC can authenticate, then switch to the production service. If UDP 7000 is unreachable or logs show persistent QUIC inactivity/timeouts, remove `transport.protocol = "quic"` to fall back to TCP. Do not repeatedly restart live network services for blind testing.

Before changing transport, back up both configurations, validate their syntax, and then restart FRPS and FRPC in order:

```bash
sudo /usr/local/bin/frps verify -c /etc/frp/frps.toml
sudo /usr/local/bin/frpc verify -c /etc/projectrebound-frpc/frpc.toml
sudo systemctl restart frps                  # run on the gateway
sudo systemctl restart projectrebound-frpc   # run on the control plane
```

## Edge nodes

All edge nodes use the same stable entrance:

```yaml
control_addr: relay.example.com:443
control_server_name: relay.example.com
```

Each node still uses an independent, one-time Bootstrap Token; after first registration the identity and client certificate are saved in the node's own persistent volume. The FRP gateway does not save node private keys and does not terminate mTLS.

## Verification

```bash
# Public DNS must return the gateway IP directly, not a Cloudflare Anycast IP
dig +short @1.1.1.1 relay.example.com A
dig +short @8.8.8.8 relay.example.com A

# Gateway: port 7000 must have both TCP and UDP listeners
sudo ss -lntup | grep -E ':(443|7000) '
sudo systemctl is-active frps

# Control plane
sudo ss -lntp | grep ':9090 '
sudo systemctl is-active projectrebound-frpc
curl -fsS http://127.0.0.1:18080/health/ready

# A connection without a client certificate must fail with "certificate required"
curl -kvsS --resolve relay.example.com:443:GATEWAY_IPV4 \
  https://relay.example.com/ --max-time 10 -o /dev/null
```

Final acceptance requires `relay control connected` in the edge-node log and node state `READY` on the control plane. The control-plane process automatically rotates the server certificate before expiration; Relay CA expiration or rotation still requires a separate dual-CA migration procedure.

After changing transport parameters, observe the system for at least 10 minutes. Confirm that FRPC/FRPS logs contain no new `session shutdown`, `timeout trying to get work connection`, or agent-disconnect events. Begin joint load testing only after every online node has recovered and telemetry is updating continuously.
