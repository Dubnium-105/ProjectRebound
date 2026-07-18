# Relay network-impairment tests

Run only on an isolated Linux test relay or network namespace. The script requires an explicit `NETEM_INTERFACE`, root privileges, and a caller-supplied integration command; it always removes its root qdisc on exit.

```sh
sudo NETEM_INTERFACE=veth-relay \
  NETEM_TEST_COMMAND='go test ./internal/relayruntime -run TestUDP -count=1' \
  ./tests/netem/run-relay-matrix.sh
```

The matrix covers 50–300 ms latency, 20–100 ms jitter, 1–5% loss, reorder, duplicate packets, constrained bandwidth, and a five-second disconnect. Verify WebSocket/control reconnection, idempotent room and allocation behavior, retryable Relay BIND, migration away from failed nodes, and tolerance of a single missed heartbeat. Never run the script on a production interface.
