# V1.1 release checklist

Record UTC timestamps and evidence links for every checked item.

- [ ] Candidate is a reviewed commit on `main`; CI and image provenance succeeded.
- [ ] Control Plane and Edge Relay image references are immutable and digests are recorded.
- [ ] Release record includes commit, build time, Go version, protocol 2, and schema 15.
- [ ] Production configuration, signing keys, Relay CA, and administrator recovery access were checked without printing secrets.
- [ ] Encrypted PostgreSQL backup, checksum, off-host copy, and verification succeeded.
- [ ] `scripts/release/preflight.sh` completed without a skipped critical check.
- [ ] V1.1 migrations were confirmed Expand/Migrate only; no Contract operation is present.
- [ ] Control Plane health, client config, internal metrics, Auth, room, WebSocket, and Relay allocation smoke tests passed.
- [ ] Canary traffic met error-rate and latency thresholds before full traffic switch.
- [ ] Relay nodes were upgraded one at a time through DRAINING → zero allocations → deploy → READY.
- [ ] Prometheus targets/rules and all V1.1 Grafana dashboards are healthy.
- [ ] Observation window completed without API, database, Redis, Relay BIND/migration, memory, or goroutine alerts.
- [ ] Previous immutable images and the tested rollback command remain available.
- [ ] Release record and operator sign-off were archived.
