# ProjectRebound

English | [简体中文](README.zh-CN.md)

[![CI and Images](https://github.com/Dubnium-105/ProjectRebound/actions/workflows/ci.yml/badge.svg)](https://github.com/Dubnium-105/ProjectRebound/actions/workflows/ci.yml)

ProjectRebound is a multi-component project containing the game payload, launch and browsing tools, a Go control plane, and independent Edge Relay nodes. The production backend uses PostgreSQL, Redis, Caddy, and immutable GHCR images; the control plane and edge nodes can run on separate hosts.

## Repository layout

| Path | Purpose |
| --- | --- |
| `Backend/` | Go control plane, Edge Relay, database migrations, Compose, monitoring, and tests |
| `Payload/`, `dxgi/` | Injected payload, runtime hooks, and proxy DLL |
| `Desktop/ProjectRebound.Browser.Python/` | Legacy Python browser compatibility prototype and portable packaging experiments |
| `ServerWrapper/`, `ServerLauncherGUI/` | Game server wrapper and launcher |
| `Tools/` | NAT/Relay validation and SDK support tools |
| `docs/` | Current architecture, API, deployment, testing, and CI/CD documentation |

## Quick verification

Backend:

```bash
cd Backend
gofmt -l .
go vet ./...
go test ./... -count=1
```

Production hosts must not rebuild the backend locally. CI publishes immutable `sha-<40-character-commit>` control-plane and Edge Relay images for every commit. Deployment workflows pull the same SHA and perform health checks, backups, and automatic rollback. See the [CI/CD guide](docs/operations/ci-cd.md).

## Documentation

Start from the [documentation center](docs/README.md). It separates current operational guidance, machine-readable contracts, version evidence, and historical archives. Content under `docs/archive/` is not a current API or deployment authority.

The documentation uses paired English and Simplified Chinese entry points. See the [documentation standard](docs/documentation-standard.md) before adding or changing public documentation.

## License

See [LICENSE.txt](LICENSE.txt).
