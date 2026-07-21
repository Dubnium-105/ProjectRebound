# V1.1 long stability gates

This harness runs the V1.1 stability gates in an isolated Docker Compose project. It does not connect to the production database, Redis, control plane, or Relay nodes. The only published port is the control plane on `127.0.0.1:38080`.

The sequence is intentionally fail-fast:

1. ten-minute preflight: 100 clients, 30 rooms, 20 Relay allocations;
2. one-hour basic gate: 100 clients, 30 rooms, 20 Relay allocations;
3. six-hour full gate: 300 clients, 100 rooms and 100 Relay allocations, with Redis and Control Plane restarted halfway through;
4. 24-hour Relay soak: 200 clients, 100 rooms and 100 Relay allocations, with Relay A and B restarted alternately every hour.

Each gate starts from fresh PostgreSQL, Redis, and Relay volumes. A gate passes only if the load report, dependency/resource telemetry, and post-cleanup database residual checks all pass. Reports include API latency and error rates, UDP delivery, Refresh Token activity, memory/goroutine trends, database pool usage, dependency availability, migrations, duplicate records, and orphan resources.

Use immutable images produced by CI:

```bash
export V11_CONTROL_PLANE_IMAGE=ghcr.io/owner/projectrebound-control-plane:sha-<full-commit>
export V11_EDGE_RELAY_IMAGE=ghcr.io/owner/projectrebound-edge-relay:sha-<full-commit>
export V11_LOAD_BOT_IMAGE=ghcr.io/owner/projectrebound-load-bot:sha-<full-commit>
export V11_LONGRUN_PROJECT=project-rebound-v11-longrun-$(date -u +%Y%m%d%H%M%S)
export V11_LONGRUN_RESULTS_DIR=/var/lib/projectrebound-longrun/$V11_LONGRUN_PROJECT
export V11_LONGRUN_HARNESS_REVISION=<full-commit>
export V11_LONGRUN_I_UNDERSTAND=isolated-docker-stack

sudo -E ./run-gates.sh
```

Run it under a service manager for the approximately 31-hour sequence. For example, a transient systemd service can execute the same environment and command. Read progress without attaching to the process:

```bash
sudo systemctl status "$V11_LONGRUN_PROJECT.service"
sudo journalctl -u "$V11_LONGRUN_PROJECT.service" -f
sudo cat "$V11_LONGRUN_RESULTS_DIR/status.env"
sudo tail -n 20 "$V11_LONGRUN_RESULTS_DIR/events.tsv"
```

The isolated configuration deliberately extends Relay Token TTL to eight hours and allocation TTL to 30 hours. The standard production TTLs are too short for a fixed-allocation soak; hourly Relay rotation still forces migrations and new token validation throughout the 24-hour gate. These values are mounted only into this disposable control plane.

After retaining the reports, remove only the exact project named in `status.env`:

```bash
docker compose --project-name "$V11_LONGRUN_PROJECT" \
  --env-file "$V11_LONGRUN_RESULTS_DIR/secrets.env" \
  --file ./docker-compose.yaml down --volumes --remove-orphans
```

Never point this harness at a shared or production environment. The project-name prefix guard and explicit acknowledgement variable are safety boundaries, not substitutes for reviewing the target host.
