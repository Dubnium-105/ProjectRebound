# V1.1 release checklist

Record UTC timestamps and evidence links for every checked item.

- [ ] Candidate is a reviewed commit on `main`; CI and image provenance succeeded.
- [ ] Control Plane and Edge Relay image references are immutable and digests are recorded.
- [ ] Release record includes commit, build time, Go version, protocol 2, and schema 16.
- [ ] Production configuration, signing keys, Relay CA, and administrator recovery access were checked without printing secrets.
- [ ] Encrypted PostgreSQL backup, checksum, off-host copy, and verification succeeded.
- [ ] `scripts/release/preflight.sh` completed without a skipped critical check.
- [ ] V1.1 migrations were confirmed Expand/Migrate only; no Contract operation is present.
- [ ] Control Plane health, client config, internal metrics, Auth, room, WebSocket, and Relay allocation smoke tests passed.
- [ ] Canary traffic met error-rate and latency thresholds before full traffic switch.
- [ ] Relay nodes were upgraded one at a time through DRAINING → zero allocations → deploy → READY.
- [ ] Every Relay reports a fresh heartbeat, `control_connected=1`, adequate certificate-expiry headroom, and a runtime-renewal-capable image.
- [ ] No cron, systemd timer, monitor, or soak runner restarts a healthy Relay; recovery runs only after confirmed process/container failure.
- [ ] A Control Plane reconnect check confirmed all Relay nodes can establish mTLS with their current certificates.
- [ ] Prometheus targets/rules and all V1.1 Grafana dashboards are healthy.
- [ ] Dynamic Grafana service targets include every online Relay from inventory rather than a fixed node list.
- [ ] Observation window completed without API, database, Redis, Relay BIND/migration, certificate-renewal, memory, or goroutine alerts.
- [ ] The 24-hour Relay soak kept healthy nodes continuously online; fault injection results were recorded separately.
- [ ] Previous immutable images and the tested rollback command remain available.
- [ ] Release record and operator sign-off were archived.
