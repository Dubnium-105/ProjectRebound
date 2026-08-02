# Dedicated Server registration and runtime identity

English | [简体中文](dedicated-server-registration.zh-CN.md)

This guide describes the implemented Dedicated Server enrollment path. The
OpenAPI contract remains authoritative for request and response fields; this
document covers operator and Windows Wrapper behavior.

## Credential flow

```text
Dedicated Server invite/grant + verified player
  -> single-use Registration Token bound to instance_id
  -> node-generated Ed25519 key and PKCS#10 CSR
  -> server_id + 24-hour runtime Token + 24-hour certificate
  -> signed heartbeat and automatic credential rotation
```

A qualifying invitation has an immutable permission snapshot containing
`allow_game_server_registration: true`. A verified player can consume that
invitation while calling `POST /v1/game-server-registration-tokens`; a player
with an existing grant omits `invite_code`. The returned `gsr_...` token:

- is bound to exactly one stable `instance_id`;
- expires after 10 minutes by default;
- is returned in a `Cache-Control: no-store` response and cannot be recovered;
- is consumed atomically by the first successful registration;
- revokes an older unconsumed token when another token is issued for the same
  instance.

An administrator with `game_servers.register` and a current MFA step-up can
instead use **Online / Dedicated Servers / Add server**. The administrator
chooses a 1–168 hour expiry and supplies an audit reason. The resulting token
has the same instance binding and single-use semantics; it does not create a
reusable global registration secret.

Example player issuance request:

```bash
curl -fsS https://api.project-rebound.space/v1/game-server-registration-tokens \
  -H "Authorization: Bearer $PLAYER_ACCESS_TOKEN" \
  -H 'Content-Type: application/json' \
  --data '{"instance_id":"hk-dedicated-01","invite_code":"REDACTED"}'
```

Never put the response Token in Git, a log, chat, or work-order body. Transfer
it only to the matching server host.

## Build and install the Windows Agent

Build the Agent from the same commit as the Wrapper/Payload. On Windows:

```powershell
New-Item -ItemType Directory -Force .\build | Out-Null
Set-Location Backend
go build -trimpath -o ..\build\game-server-agent.exe .\cmd\game-server-agent
```

For a Windows cross-build from Linux:

```bash
mkdir -p build
(cd Backend && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 \
  go build -trimpath -o ../build/game-server-agent.exe ./cmd/game-server-agent)
```

Place `game-server-agent.exe` beside the Dedicated Server executable, or set an
explicit path in the Wrapper configuration. The Agent is a real Control Plane
client: it generates the node key locally, submits the CSR, persists the
issued identity, signs heartbeats, and rotates credentials.

## Wrapper configuration

Add these fields to `serverconfig.json`:

```json
{
  "backend": "https://api.project-rebound.space",
  "registrationToken": "gsr_REPLACE_WITH_ONE_TIME_TOKEN",
  "serverId": "hk-dedicated-01",
  "publicHost": "203.0.113.10",
  "maxPlayers": 10,
  "gameServerAgent": "game-server-agent.exe"
}
```

`serverId` must exactly match the Registration Token's `instance_id`.
`publicHost` must be the public unicast address advertised by the game server;
the backend rejects loopback and private addresses. Existing Wrapper fields
provide the display name, region, game mode, external port, and other launch
settings.

The equivalent command-line overrides are:

| JSON field | Wrapper/Payload argument |
| --- | --- |
| `registrationToken` | `-registrationtoken=<token>` |
| `serverId` | `-serverid=<instance_id>` |
| `publicHost` | `-publichost=<address>` |
| `maxPlayers` | `-maxplayers=<count>` |
| `gameServerAgent` | `-gameserveragent=<path>` |

JSON is loaded first and a non-empty argument overrides it. The Wrapper passes
the one-time Token to the Payload, which exposes it to the Agent only through
`GAME_SERVER_REGISTRATION_TOKEN`. The Token is not written to the Agent command
line or logs. Prefer a locally ACL-restricted configuration file for the first
enrollment: the compatibility `-registrationtoken=` override can be visible to
other local processes that are allowed to inspect command lines.

For service managers that invoke the Agent directly, its complete flag surface
is:

| Agent flag | Default/purpose |
| --- | --- |
| `-control-plane-url` | `http://127.0.0.1:8080`; primary API base URL |
| `-fallback-control-plane-url` | empty; heartbeat-only fallback |
| `-identity-file` | `game-server-identity.json` |
| `-instance-id` | required for first enrollment |
| `-display-name` | `Dedicated Server` |
| `-region` | `asia-hk` |
| `-mode` | `tdm` |
| `-version` | `1.0.0` |
| `-public-host` | required for first enrollment |
| `-public-port` | `7777` |
| `-max-players` | `16` |
| `-rotate-before` | `6h` |
| `-heartbeat-state` | `READY` |
| `-player-count` | `0` |
| `-once` | send one heartbeat and exit instead of running continuously |

Direct invocation still obtains the first-use secret only from
`GAME_SERVER_REGISTRATION_TOKEN`; inject that environment variable through the
service's secret mechanism and remove it after enrollment. Do not add a Token
flag to a wrapper script.

## Runtime behavior and secret storage

The Payload launches the Agent in `-once` mode no more than once every 15
seconds after a successful run. The Wrapper supplies the current player count
and reports `READY` when the server has no players, then `RUNNING` once at least
one player is present. On the first run the Agent requires the Registration
Token, `serverId`, and `publicHost`. Subsequent runs use the identity file and
do not require the Registration Token.

For the standard production backend, the Wrapper supplies
`https://cnapi.project-rebound.space` as a fallback. The Agent uses that
fallback only for an idempotent signed heartbeat. Enrollment and credential
rotation always use the configured primary Control Plane so a timeout cannot
create ambiguous credentials. A custom backend has no implicit production
fallback.

The Wrapper names the identity file
`game-server-identity-<sanitized-serverId>.json`. It contains the node private
key, runtime Token, certificate, CA certificate, generation, and expiry times.
The Agent writes it atomically and restricts it to the current Windows user;
non-Windows builds use mode `0600`. Back up the file only into approved secret
storage. Do not copy one identity to a different instance.

The default runtime Token and certificate lifetime is 24 hours. The Agent
rotates both when either has six hours or less remaining, using a newly
generated Ed25519 key. The previous credential pair is accepted for ordinary
runtime traffic for 60 seconds, but cannot rotate again or deregister the
server.

After the identity file exists, the Payload clears the Registration Token from
its environment and memory. It cannot safely rewrite the separate Wrapper
configuration process, so the operator must remove `registrationToken` from
`serverconfig.json` and restart the Wrapper. The one-time Token is already
consumed, but removing it reduces local secret exposure.

## Production Control Plane prerequisite

Production refuses to initialize the game-server certificate authority unless
both `GAME_SERVER_CA_CERT_PEM_BASE64` and `GAME_SERVER_CA_KEY_PEM_BASE64` are
present and form a matching CA pair. New environments created by
`Backend/scripts/generate-control-plane-env.sh` contain both values. When
upgrading an existing environment, add a separately generated Game Server CA
without replacing any existing Access, Relay, update, database, or MFA secret.

Keep this CA stable across image releases and host rebuilds. Replacing or losing
it prevents normal renewal of certificates issued by the previous CA. Keep the
environment file at mode `0600`, back it up through the normal encrypted backup
process, and never copy the Game Server CA private key to a Dedicated Server,
MetaServer, gateway, or GitHub Actions log.

The legacy `GAME_SERVER_REGISTRATION_TOKENS` environment variable is not read
by the current implementation. Registration credentials are database-backed,
instance-bound, single-use records issued by the player or administrator APIs.

## Verification and troubleshooting

After enrollment:

1. confirm the identity file exists with a restricted ACL;
2. remove the one-time Token from the Wrapper configuration;
3. confirm the server appears in `GET /v1/game-servers`;
4. confirm `player_count` and `READY`/`RUNNING` follow the live server;
5. verify no Token, private key, or complete identity document appears in logs.

If the server remains unlisted, check the Wrapper log for missing `serverId`,
`publicHost`, Agent path, or Registration Token. An HTTP registration failure
does not justify switching enrollment to the fallback endpoint or issuing an
unbound static token. Issue a new instance-bound token if the previous one
expired or its outcome is known to be unsuccessful.

See [External API](../api/external.md), [Internal API](../api/internal.md), and
the [deployment guide](deployment-guide.md) for the complete HTTP and host
contracts.
