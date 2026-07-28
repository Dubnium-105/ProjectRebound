# MetaServer native protocol

English | [简体中文](metaserver-native-protocol.zh-CN.md)

## Transport

Clients connect only through MetaTunnel to `logic.dubnium.top:443`. MetaTunnel
validates the public certificate with the Windows system trust store and TLS
server name `logic.dubnium.top`. The gateway terminates TLS and forwards the
byte stream through the isolated, TLS-enabled and token-authenticated Meta FRP
channel. No plaintext native listener is public.

Each frame is:

```text
uint32_be payload_length | protobuf RequestWrapper payload
```

The parser accepts fragmented and coalesced TCP reads. Payload length must be
between 1 and 2 MiB. A connection has handshake, frame-read, frame-write, and
idle deadlines. Responses are serialized through one bounded write queue.

## Gate

The first state-changing request must carry the single-use Gate Ticket returned
by the authenticated HTTP session endpoint. The server atomically consumes the
ticket and binds the connection to its player ID, auth session ID, client
version, protocol version, and issue time. The following terminate the
connection:

- expired, unknown, concurrent, or replayed ticket;
- protocol-version mismatch;
- identity mismatch in a legacy message;
- repeated malformed frames or protobuf;
- rate-limit or write-queue abuse.

## Confirmed RPC behavior

The production wrapper maps only reviewed RPC identifiers. Gate/status,
Party creation/readiness/presence, region discovery, playlist discovery,
matchmaking start, and matchmaking status/stop compatibility responses use
statically generated messages. Unknown RPCs receive a compatibility error
without crashing the connection.

`QueryUnityMatchmakingRes` fields whose upstream numbers remain tentative are
not used to publish a match endpoint. The authoritative endpoint is available
through the authenticated HTTP ticket resource until a sanitized capture fixes
the native field mapping. This prevents an unverified field guess from entering
the production state machine.

## Packet handling rules

- Never log a complete frame or decoded loadout.
- Never echo unknown fields into a different player session.
- A keepalive does not extend Access Token or Gate Ticket validity.
- Frame and RPC limits apply before expensive protobuf or database work.
- Golden samples in `Backend/internal/metaserver/testdata/golden` are sanitized
  and contain no production identity or credentials.

Protocol provenance and the exact upstream commit are recorded in
`Backend/api/proto/metaserver/UPSTREAM.md`.
