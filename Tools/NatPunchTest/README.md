# ProjectRebound legacy NAT punch test

English | [简体中文](README.zh-CN.md)


> [!WARNING]
> This tool verifies guest, room, NAT rendezvous, and embedded UDP Relay behavior through the legacy single-process Go compatibility entry point. It does not verify the current standalone Edge Relay and is not a production acceptance test.

## Applicable environment

When the legacy compatibility entry point is run from `Backend/`, the default endpoints are:

```text
HTTP  http://127.0.0.1:5000
UDP   5001 rendezvous
UDP   5002 embedded relay
```

The current `cmd/control-plane` + `cmd/edge-relay` split architecture uses different protocols and ports. To verify it, use `Backend/tests/netem/` and `Backend/api/relay-protocol.md`.

## Local smoke test

```powershell
Tools\NatPunchTest\run-loopback.bat --backend http://127.0.0.1:5000
```

A successful run ends with `PASS: received pong ...`. This proves only that local HTTP/UDP compatibility paths work; it does not prove cross-NAT reachability.

## Direct connection test between two machines

Host A:

```powershell
Tools\NatPunchTest\run-host.bat --backend http://LEGACY_SERVER:5000 --port 27777
```

Record the output `ROOM_ID`. On host B:

```powershell
Tools\NatPunchTest\run-client.bat --backend http://LEGACY_SERVER:5000 --room-id ROOM_ID --port 27778
```

Add `--relay` at both ends to verify the legacy embedded UDP Relay on port 5002. Do not use this option for the current Edge Relay.

## Failure meaning

- `UDP rendezvous timed out`: legacy UDP port 5001 did not complete a round trip.
- `NAT_BINDING_NOT_READY`: the compatibility backend did not observe the corresponding binding.
- `FAIL: no pong`: hole punching, routing, or a firewall blocked the packet.
- `--relay` failure: legacy UDP port 5002 is not running, is blocked, or failed to register.

The script relies only on the Python standard library. It remains in the repository for regression compatibility and should not drive new client or deployment designs.
