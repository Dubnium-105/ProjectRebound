# ProjectRebound documentation center

English | [简体中文](README.zh-CN.md)

This is the entry point for the whole project. Start with the [system overview](architecture/overview.md), then choose API, operations, testing, or component documentation. Historical material is isolated under [`archive/`](archive/README.md) and must not be used as current implementation or deployment guidance.

## Authority order

When sources disagree, use this order:

1. machine-readable contracts: `Backend/api/openapi/openapi.yaml` and `Backend/api/proto/relay_control.proto`;
2. current implementation, database migrations, and automated tests;
3. `docs/api/`, `docs/architecture/`, and `docs/operations/`;
4. version-specific evidence under `docs/testing/`;
5. `docs/archive/` for historical context only.

## Start by role

| Role or task | Start here |
| --- | --- |
| New developer learning the system | [System overview](architecture/overview.md) |
| Game client or dedicated-server integration | [API documentation](api/README.md) |
| Deploy control plane, public gateway, or Relay | [Deployment entry point](operations/deployment.md) |
| Release, rollback, or incident response | [Operations documentation](operations/README.md) |
| Validate a V1.1 candidate | [V1.1 validation index](testing/v1.1/README.md) |
| Maintain a specific component | The `README.md` beside that component |

## Information architecture

| Directory | Content |
| --- | --- |
| [`architecture/`](architecture/README.md) | System boundaries, authentication, Relay protocol, migration, and desktop runtime commands |
| [`api/`](api/README.md) | External HTTP/WebSocket, internal administration, Relay HTTP/mTLS API |
| [`operations/`](operations/README.md) | Deployment, CI/CD, releases, backup, certificates, and continuity policy |
| [`operations/runbooks/`](operations/runbooks/README.md) | Production incident response and recovery |
| [`testing/`](testing/README.md) | Test strategy, version evidence, and release gates |
| [`archive/`](archive/README.md) | Superseded API, architecture, audit, and implementation snapshots |

## Component documentation

Implementation-specific documents remain close to their code:

- `Backend/api/openapi/auth-permission-matrix.md`: authorization matrix;
- `Backend/api/relay-protocol.md`: machine-level UDP data-plane protocol;
- `Backend/deployments/README.md`: Compose and deployment assets;
- `Backend/tests/integration/README.md`: real control-plane/two-Relay gate;
- `Backend/tests/load/README.md`: load and stability tests;
- `Backend/tests/netem/README.md`: weak-network matrix;
- READMEs under `Tools/`: standalone diagnostic tools.

## Maintenance rules

- Follow the [bilingual documentation standard](documentation-standard.md).
- Update OpenAPI/proto first for contract changes, then synchronize human-readable API docs and tests.
- Architecture documents describe stable boundaries; do not record one-off host IPs, temporary tokens, or implementation logs.
- Production examples use immutable commit SHAs or digests, never `latest` or on-host builds.
- Healthy Relay nodes are never restarted on a schedule; continuous soak and fault injection are separate tests.
- Move superseded documents to `archive/` and identify their replacement.
- Before committing, run both documentation checks listed in the documentation standard.
