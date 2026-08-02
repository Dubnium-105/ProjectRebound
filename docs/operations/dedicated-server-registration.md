# Dedicated Server registration and runtime identity

English | [简体中文](dedicated-server-registration.zh-CN.md)

This guide describes the production registration path implemented by the Rust
Toolbox and the in-process Payload. The OpenAPI contract remains authoritative
for HTTP fields.

## Credential flow

```text
verified player + Dedicated Server invitation/grant
  -> single-use Registration Token bound to instance_id
  -> Rust Toolbox generates an Ed25519 key and PKCS#10 CSR
  -> server_id + runtime Token + node certificate
  -> Toolbox reads non-secret game status over a per-launch named pipe
  -> signed heartbeat + automatic key/Token/certificate rotation
```

A qualifying invitation has an immutable permission snapshot containing
`allow_game_server_registration: true`. A verified player can redeem that
invitation while calling `POST /v1/game-server-registration-tokens`; a player
with an existing grant omits `invite_code`. The returned `gsr_...` token:

- is bound to one stable `instance_id`;
- expires after 10 minutes by default;
- is returned once with `Cache-Control: no-store` and cannot be recovered;
- is consumed atomically by the first successful registration;
- revokes an older unconsumed token issued for the same instance.

An administrator with `game_servers.register` and a current MFA step-up can
instead use **Online / Dedicated Servers / Add server**. The administrator
chooses a 1–168 hour expiry and supplies an audit reason. This produces the same
instance-bound, single-use credential, not a reusable global secret.

Example player issuance request:

```bash
curl -fsS https://api.project-rebound.space/v1/game-server-registration-tokens \
  -H "Authorization: Bearer $PLAYER_ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"instance_id":"hk-dedicated-01","invite_code":"REDACTED"}'
```

Never put the response Token in Git, logs, chat, or a work-order body. Transfer
it only to the matching Windows host and enter it into the Toolbox-managed
configuration.

## Toolbox configuration

Use the Rust Toolbox from `ProjectReboundToolbox`. No separate
`game-server-agent.exe` is installed or launched in the production flow.

Configure `ServerLauncher/serverconfig.json`:

```json
{
  "backend": "https://api.project-rebound.space",
  "offline": false,
  "registrationToken": "gsr_REPLACE_WITH_ONE_TIME_TOKEN",
  "serverId": "hk-dedicated-01",
  "serverName": "Hong Kong Dedicated 01",
  "serverRegion": "asia-hk",
  "mode": "pvp",
  "gameVersion": "0.7.0",
  "publicHost": "203.0.113.10",
  "externalPort": 7777,
  "maxPlayers": 10
}
```

`serverId` is the stable `instance_id` and must exactly match the Registration
Token binding. It is not the backend-generated `server_id`. `publicHost` must
be a public unicast address; the backend rejects loopback and private
addresses. The Toolbox preserves existing configuration when changing the game
mode.

The one-time Token is a Toolbox input only. The Wrapper no longer accepts or
forwards `-registrationtoken`, and the Payload never reads
`GAME_SERVER_REGISTRATION_TOKEN`. Do not place the Token on any process command
line.

## Named-pipe registration channel

For every server launch, the Toolbox generates a 192-bit random pipe suffix and
passes only `-pipe=<name>` to the Wrapper. The Wrapper forwards that pipe name
to the game process. The Payload creates `\\.\pipe\<name>` as a one-instance,
duplex, message-mode pipe and restricts it to the same Windows user and session.

The registration worker connects as the client and uses this request:

```text
server_status\t{"request_id":"server-status-1"}\n
```

The Payload returns only non-secret runtime state:

```text
server_status_ack\t{"state":"RUNNING","player_count":2,"round_state":"InProgress","request_id":"server-status-1"}\n
```

`state` is `READY` with no connected players and `RUNNING` otherwise. No
Registration Token, node private key, runtime Token, certificate, CSR, or
signature is sent through the pipe. The Toolbox owns all HTTP and credential
operations. A process running as the same user can still connect if it learns
the random name, so the pipe contract deliberately contains no long-lived
secret.

## Runtime behavior and secret storage

The Toolbox waits for the Payload pipe before enrollment. It then:

1. generates an Ed25519 key locally and submits a PKCS#10 CSR to the configured
   primary Control Plane;
2. persists the issued identity as
   `game-server-identity-<sanitized-instance_id>.dpapi` beside
   `serverconfig.json`;
3. encrypts the complete identity with current-user Windows DPAPI and replaces
   the file atomically;
4. removes `registrationToken` from `serverconfig.json` after the identity is
   safely stored;
5. polls live state through the named pipe and sends signed heartbeats at the
   server-provided interval;
6. rotates to a newly generated Ed25519 key when the runtime Token or
   certificate has six hours or less remaining.

Enrollment and rotation use only the configured primary Control Plane so a
timeout cannot create ambiguous credentials. For the two standard production
URLs, idempotent signed heartbeats may fall back between
`https://api.project-rebound.space` and
`https://cnapi.project-rebound.space`. A custom backend has no implicit
fallback. Transient heartbeat and rotation failures are logged and retried
without moving private-key operations into the Payload.

Runtime Tokens and certificates default to a 24-hour lifetime. The previous
credential pair is accepted for ordinary runtime traffic for 60 seconds after
rotation, but cannot rotate again or deregister the server.

DPAPI binds the identity to the Windows user profile. Back up the file only as
part of an approved host/profile backup, and test restoration under the same
service identity. Never copy one node identity to another instance. Losing the
file requires an authorized re-enrollment with a fresh single-use Registration
Token.

The Go command under `Backend/cmd/game-server-agent` remains a backend protocol
reference and developer diagnostic client. It is not part of the Toolbox
production launch path.

## Production Control Plane prerequisite

Production refuses to initialize the game-server certificate authority unless
both `GAME_SERVER_CA_CERT_PEM_BASE64` and
`GAME_SERVER_CA_KEY_PEM_BASE64` are present and form a matching CA pair. New
environments created by `Backend/scripts/generate-control-plane-env.sh` contain
both values. When upgrading an existing environment, add a separately generated
Game Server CA without replacing any Access, Relay, update, database, or MFA
secret.

Keep this CA stable across image releases and host rebuilds. Store the
environment file at mode `0600` and back it up through the normal encrypted
backup process. Never copy the Game Server CA private key to a Dedicated
Server, Toolbox host, MetaServer, gateway, or CI log.

The legacy `GAME_SERVER_REGISTRATION_TOKENS` environment variable is not read.
Registration credentials are database-backed, instance-bound, single-use
records issued by the player or administrator APIs.

## Verification and troubleshooting

After enrollment:

1. confirm `registrationToken` was removed from `serverconfig.json`;
2. confirm the `.dpapi` identity file exists and is not readable as JSON;
3. confirm the Toolbox log shows a Payload pipe connection and accepted signed
   heartbeat;
4. confirm the server appears in `GET /v1/game-servers`;
5. confirm `player_count` and `READY`/`RUNNING` follow the live server;
6. verify no Token, private key, CSR, signature, or identity document appears
   in Wrapper/Payload logs or process command lines.

If the server remains unlisted, check `serverId`, `publicHost`, `offline`, the
primary backend URL, and whether the one-time Token expired. A missing
`server_status_ack` indicates a Toolbox/Wrapper/Payload version mismatch or
missing `-pipe` forwarding. Do not switch enrollment to the fallback endpoint
or issue an unbound static token. Issue a new instance-bound token only when the
previous token is known to have expired or failed before registration.

See [External API](../api/external.md), [Internal API](../api/internal.md), the
[named-pipe protocol](../architecture/command-framework.md), and the
[deployment guide](deployment-guide.md) for the complete contracts.
