# Edge Relay runtime

For a relay running on a separate Linux host, use `../edge-relay/docker-compose.yaml` and the complete procedure in `../../../docs/operations/deployment-guide.md`. This directory contains the image definition and protocol runtime assets; the optional relay profile in the development Compose file remains intended only for local integration.

The image contains one statically linked Go binary plus the system CA bundle required for HTTPS enrollment. It does not connect to PostgreSQL, Redis, NATS, or game services. Persistent local state is limited to the node's private key, mTLS certificate, opaque node credential, CA, and relay-token public keyset in `identity.json`.

Mount `config.edge-relay.yaml` read-only at `/etc/projectrebound/config.edge-relay.yaml` and mount a node-local writable directory at the configured `data_dir`. Provide the one-time `EDGE_RELAY_BOOTSTRAP_TOKEN` only for first enrollment, then remove it from the deployment secret set.

Expose UDP 8443 as the desired external UDP port (normally 443). Keep the metrics listener bound to loopback. Allow outbound HTTPS to the enrollment endpoint and outbound mTLS gRPC to the configured control address.

`scripts/deploy-edge-relay.sh` prefers Docker Compose v2, supports the standalone `docker-compose` command, and falls back to an equivalent isolated `docker run` deployment when neither Compose implementation is installed. Set `EDGE_RELAY_RUNTIME=compose` or `EDGE_RELAY_RUNTIME=raw-docker` to require one mode explicitly. Raw Docker mode uses the stable container name `project-rebound-edge-relay` and persistent volume `project-rebound-edge-relay-data`; both can be overridden with `EDGE_RELAY_CONTAINER_NAME` and `EDGE_RELAY_VOLUME_NAME`.
