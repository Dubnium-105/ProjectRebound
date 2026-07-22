# Relay Protocol V2

English | [简体中文](relay-protocol.zh-CN.md)

For the authoritative wire format corresponding to the implementation, see [`Backend/api/relay-protocol.md`](../../Backend/api/relay-protocol.md). This page explains the compatibility boundaries between clients and operators.

The production protocol version is fixed to `2`, and the default is `accept_protocol_v1: false`. The handshake sequence is `BIND_INIT → BIND_CHALLENGE → BIND_PROOF → BIND_OK`; Challenge Cookie binds source IP/port, nonce, MTU, Token hash and short-time bucket, and Relay does not create allocation before Proof. Invalid cookies, authentication tags, handles, replay sequences, and oversized packets are silently discarded to avoid UDP reflections.

Relay Token is a short-term Ed25519 credential bound to the node, allocation, connection and HOST/PEER roles, and comes with `kid`, `jti`, `nbf`/`exp`, PPS/BPS/total byte caps. Packets use a random 64-bit handle, per-end session key, sequence window, and 16-byte HMAC tag; there is no destination address within the packet, so it can only be forwarded between authenticated HOST/PEERs in the same allocation. The default Payload MTU is 1200 bytes, and the configurable range is 1000~1350.

V1 is only used for short-term migrations that are explicitly enabled. It does not have the data plane guarantee of V2 and cannot be enabled in production. V1.1 does not promise transmission encryption, reliable retransmission, sequence guarantee, or lossless migration; the game payload is still protected by the end-to-end game protocol itself.

For failover, see [Relay Failover Migration](relay-migration.md), for signature Keyset and node certificate, see [Relay Key Rotation](../operations/key-and-certificate-rotation.md), for exception handling, see [Relay Fault Runbook](../operations/runbooks/relay-outage.md).
