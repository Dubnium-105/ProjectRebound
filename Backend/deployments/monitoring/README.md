# ProjectRebound monitoring

English | [简体中文](README.zh-CN.md)

The supplied Prometheus configuration scrapes the control plane internally and optionally discovers direct Edge Relay targets from `targets/edge-relays.yml`. The control plane already exports inventory, lifecycle, capacity, mTLS connectivity, and relayed `TrafficReport` telemetry for every registered node, so direct scraping is not required for a newly added relay. Keep the discovery file empty when no node-local metrics transport is configured. Direct targets remain useful for troubleshooting and should carry stable `instance`, `node_id`, `region`, and `environment` labels.

The Edge Relay process intentionally keeps its metrics listener on loopback. Do not change `metrics_addr` to a public address. Run a node-local proxy or monitoring agent that reads `127.0.0.1:<metrics-port>` and exposes it only over an authenticated private network such as Tailscale or WireGuard. Prometheus should never scrape relay metrics over the public interface.

The Grafana provisioning directory installs the `project-rebound-prometheus` data source, the dynamic **Project Rebound Operations** fleet view, and eight V1.1 drill-down dashboards: Control Plane Overview, Authentication and Session Security, P2P Rooms and Connections, Relay Fleet Overview, Relay Security, Relay Traffic and Capacity, Database and Redis, and Release and Update Status. The operations dashboard covers:

- repeated service-target cards for the control plane and every online relay, plus a detailed status card for each relay whose mTLS control connection is online; Grafana adds and wraps both card groups automatically as the online node set changes, while the inventory table continues to show every registered node, including offline and revoked nodes;
- control-plane HTTP traffic, P95 latency, sessions, rooms, allocations, security failures, database pool and relay registry state;
- Edge Relay control connectivity, reconnects, allocations, packet forwarding/drops, traffic, invalid tokens and rate limiting;
- control-plane and relay-host CPU, memory, root filesystem and network throughput when node-exporter jobs use the `project-rebound-node.*` naming convention.

Prometheus loads `alerts/v1.1.rules.yml`. It covers API availability/latency, PostgreSQL and Redis, pool and disk pressure, authentication abuse, Relay availability/security/capacity/migration, and backup freshness. Route alerts to the operator's Alertmanager in production; the repository intentionally does not contain notification credentials.

Backup scripts can publish textfile-collector metrics by setting `BACKUP_METRICS_DIRECTORY` to the node-exporter textfile directory (commonly `/var/lib/node_exporter/textfile_collector`). Configure node-exporter with `--collector.textfile.directory` for that same directory. The backup and restore scripts atomically update independent `.prom` files, so a failed run does not erase the timestamp of the previous successful backup.

For a separate Prometheus/Grafana installation, copy the dashboard and provisioning files into that stack, point the data source at its Prometheus URL, and add these jobs:

- `project-rebound-control-plane` with `/internal/metrics` on the private control-plane listener;
- `project-rebound-edge-relay` through the node-local private metrics transport;
- `project-rebound-node-control-plane` and `project-rebound-node-edge-relay` for host resource metrics.

Validate changes before reloading:

```bash
promtool check config /etc/prometheus/prometheus.yml
promtool check rules /etc/prometheus/alerts/v1.1.rules.yml
curl -fsS http://127.0.0.1:9090/api/v1/targets
curl -fsS http://127.0.0.1:9090/api/v1/rules
curl -fsS http://127.0.0.1:3000/api/health
```
