# Edge Relay UDP protocol v1

All integers are unsigned and big-endian. Datagrams start with `PRLY`, protocol version `1`, and a one-byte message type. Invalid datagrams are dropped without a response.

## Bind challenge

1. `BIND (1)`: `magic[4] | version[1] | type[1] | token_length[2] | relay_token[n]`
2. `CHALLENGE (2)`: `magic[4] | version[1] | type[1] | cookie[32]`
3. `BIND_PROOF (3)`: `magic[4] | version[1] | type[1] | cookie[32] | token_length[2] | relay_token[n]`
4. `BIND_OK (4)`: `magic[4] | version[1] | type[1] | allocation_handle[8] | endpoint_role[1]`

The cookie is an HMAC over the source IP/port, token hash, and a short time bucket. The challenge is smaller than the request. A valid signed token binds exactly one `HOST` or `PEER` endpoint. A retry from the same source is idempotent; reuse from a different source is rejected.

## Data packet

`DATA (5)` uses:

```text
magic[4] | version[1] | type[1] | allocation_handle[8] |
endpoint_role[1] | sequence[8] | authentication_tag[16] | opaque_payload[n]
```

The endpoint key is `HMAC-SHA256(relay_token, "project-rebound-relay-data-v1")`. The authentication tag is the first 16 bytes of HMAC-SHA256 over the header excluding the tag plus the opaque payload. The relay authenticates and re-tags packets for the bound recipient without parsing or decrypting game payloads.

Forwarding begins only after both roles bind. The packet has no destination-address field, so traffic can only move between the two endpoints of one allocation. The relay rejects unknown handles, wrong roles or sources, invalid tags, duplicate/out-of-window sequences, expired/idle allocations, and packets exceeding per-IP, PPS, bandwidth, or total-byte limits.
