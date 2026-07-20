# ProjectRebound monitoring

The supplied Prometheus configuration scrapes the control plane internally and optionally discovers direct Edge Relay targets from `targets/edge-relays.yml`. The control plane already exports inventory, lifecycle, capacity, mTLS connectivity, and relayed `TrafficReport` telemetry for every registered node, so direct scraping is not required for a newly added relay. Keep the discovery file empty when no node-local metrics transport is configured. Direct targets remain useful for troubleshooting and should carry stable `instance`, `node_id`, `region`, and `environment` labels.

The Edge Relay process intentionally keeps its metrics listener on loopback. Do not change `metrics_addr` to a public address. Run a node-local proxy or monitoring agent that reads `127.0.0.1:<metrics-port>` and exposes it only over an authenticated private network such as Tailscale or WireGuard. Prometheus should never scrape relay metrics over the public interface.

The Grafana provisioning directory installs the `project-rebound-prometheus` data source and the **Project Rebound Operations** dashboard. The dashboard covers:

- a repeated status card for every relay whose mTLS control connection is online; Grafana adds and wraps cards automatically as the online node set changes, while the inventory table continues to show every registered node, including offline and revoked nodes;
- control-plane HTTP traffic, P95 latency, sessions, rooms, allocations, security failures, database pool and relay registry state;
- Edge Relay control connectivity, reconnects, allocations, packet forwarding/drops, traffic, invalid tokens and rate limiting;
- control-plane and relay-host CPU, memory, root filesystem and network throughput when node-exporter jobs use the `project-rebound-node.*` naming convention.

For a separate Prometheus/Grafana installation, copy the dashboard and provisioning files into that stack, point the data source at its Prometheus URL, and add these jobs:

- `project-rebound-control-plane` with `/internal/metrics` on the private control-plane listener;
- `project-rebound-edge-relay` through the node-local private metrics transport;
- `project-rebound-node-control-plane` and `project-rebound-node-edge-relay` for host resource metrics.

Validate changes before reloading:

```bash
promtool check config /etc/prometheus/prometheus.yml
curl -fsS http://127.0.0.1:9090/api/v1/targets
curl -fsS http://127.0.0.1:3000/api/health
```
