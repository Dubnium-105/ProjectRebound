# Edge Relay UDP protocol v2

All integers are unsigned and big-endian. Datagrams start with `PRLY`, protocol version `2`, and a one-byte message type. Invalid datagrams are dropped without a response. Production defaults to `accept_protocol_v1: false`; the legacy v1 format is available only as a temporary, explicit compatibility mode.

## Bind challenge

1. `BIND_INIT (1)`: `magic[4] | version[1] | type[1] | client_nonce[16] | requested_mtu[2] | token_length[2] | relay_token[n]`
2. `BIND_CHALLENGE (2)`: `magic[4] | version[1] | type[1] | server_nonce[16] | expires_in_ms[4] | cookie[32]`
3. `BIND_PROOF (3)`: `magic[4] | version[1] | type[1] | client_nonce[16] | server_nonce[16] | requested_mtu[2] | cookie[32] | token_length[2] | relay_token[n]`
4. `BIND_OK (4)`: `magic[4] | version[1] | type[1] | allocation_handle[8] | endpoint_role[1] | negotiated_mtu[2]`

The cookie is an HMAC over a domain separator, source IP/port, both nonces, requested MTU, token hash (which covers the allocation claim), and a short time bucket. Relay accepts the current and previous bucket. The challenge has a 5–15 second configured lifetime, is never larger than `BIND_INIT`, and creates no allocation or per-challenge server state. Invalid cookies are silently dropped without a detailed error.

A valid signed token binds exactly one `HOST` or `PEER` endpoint. The Relay verifies signature, issuer, audience, key ID, `jti`, node/allocation/connection identity, endpoint role, protocol, `nbf`/`exp`, and all traffic limits before allocating state. A `jti` retry from the same endpoint is idempotent. A newly challenged source port on the same IP may replace the endpoint only during the configured short NAT-rebind window; cross-IP or late reuse is rejected and counted. The in-memory replay cache has both TTL cleanup and a hard entry cap.

## Data packet

`DATA (5)` uses:

```text
magic[4] | version[1] | type[1] | allocation_handle[8] |
endpoint_role[1] | flags[1] | sequence[8] | authentication_tag[16] | opaque_payload[n]
```

The v2 endpoint key is `HMAC-SHA256(relay_token, "project-rebound-relay-data-v2")`. The authentication tag is the first 16 bytes of HMAC-SHA256 over the header (including flags but excluding the tag) plus the opaque payload. V1.1 requires flags to be zero; packets with unknown flag bits are dropped. The relay authenticates and re-tags packets for the bound recipient without parsing or decrypting game payloads.

Forwarding begins only after both roles bind. The packet has no destination-address field, so traffic can only move between the two endpoints of one allocation. The relay rejects unknown handles, wrong roles or sources, invalid tags, duplicate/out-of-window sequences, expired/idle allocations, and packets exceeding per-IP, PPS, bandwidth, or total-byte limits.

The negotiated opaque payload MTU is 1000–1350 bytes and defaults to 1200. Oversized packets are dropped without a response. Allocation handles are random non-zero 64-bit values and become invalid immediately when an allocation closes.

## Resource limits

Unverified sources have separate token buckets for `BIND_INIT`, `BIND_PROOF`, malformed traffic, and invalid signed-token attempts. Repeated invalid tokens temporarily ban new handshake traffic from that IP; already authenticated data still passes its endpoint and node buckets. The IP-state table has a hard cardinality cap and idle cleanup. A configurable per-IP limit counts unique allocations, so HOST and PEER behind the same NAT do not count twice for one allocation.

Each endpoint enforces token-claimed PPS and bytes-per-second buckets. Each allocation enforces an absolute expiry, idle timeout, and total-byte ceiling; crossing the total-byte ceiling immediately closes the allocation and invalidates its handle. Node-wide ingress PPS and egress byte buckets protect existing allocations from unbounded aggregate traffic. Cleanup uses the shared sweeper and does not create a goroutine or ticker per allocation.

## Overload states

At every heartbeat interval the relay derives its load from allocation count, ingress and egress rates, ingress PPS, and Go heap allocation. The maximum utilization selects `NORMAL`, `DEGRADED`, or `REJECT_NEW`; an operator drain selects `DRAINING`. Thresholds and capacity denominators are explicit configuration values.

`REJECT_NEW` and `DRAINING` reject only the first endpoint of a new allocation. They continue to accept the second endpoint and authenticated data for an allocation already installed on the node. The state is exposed on local Prometheus metrics and sent over the authenticated control stream. The control plane persists it and excludes `REJECT_NEW` and `DRAINING` nodes from initial placement and migration targets.
