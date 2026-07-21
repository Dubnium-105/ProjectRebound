# V1.1 deployment entry point

ProjectRebound uses three independently deployed roles:

1. the private Control Plane host runs PostgreSQL, Redis, Control Plane, Caddy, Prometheus, and Grafana with the separated Compose file;
2. the public gateway forwards HTTP and raw TCP mTLS without terminating Relay client certificates;
3. each Edge Relay runs only the Relay process on Linux host networking and opens its UDP gameplay port publicly.

The authoritative host preparation, firewall, FRP/HAProxy, certificate, DNS, and first-enrollment procedure is [Debian deployment and operations](debian-deployment-and-ops.md). CI/CD publishes immutable `sha-<commit>` and semantic-version images plus provenance and build records; production hosts pull these artifacts rather than compiling. See [CI/CD](cicd.md).

For V1.1 production changes:

```bash
# Control Plane: encrypted backup, preflight, compatible migration, deploy,
# smoke test, observation, automatic previous-image rollback on failure.
cd Backend
scripts/release/control-plane.sh

# One Relay at a time: drain/migrate, wait for zero allocations, deploy,
# reconnect and resume.
scripts/release/rolling-edge-relay.sh
```

Required release environment and rollback behavior are documented in [V1.1 release and rollback](release-and-rollback.md). Complete the [release checklist](v1.1/release-checklist.md) and do not promote a build while the [test report](v1.1/test-report.md) or [restore report](v1.1/restore-test-report.md) contains a required `NOT_RUN`/`FAIL` gate.

New Relay operators need only a node-specific bootstrap token, a small `.env`, and the Relay YAML. After one enrollment the token is removed and identity/certificate rotation use the existing mTLS control stream. No Relay needs PostgreSQL, Redis, Cloudflare Zero Trust, or a public metrics listener.
