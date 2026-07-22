# Deployment entry points

English | [简体中文](README.zh-CN.md)

- Local all-in-one development: `compose/docker-compose.yaml`
- Separated control plane: `control-plane/docker-compose.yaml`
- Separated Linux edge relay: `edge-relay/docker-compose.yaml`
- Prometheus and Grafana: `monitoring/README.md`
- Full Debian procedure: `../../docs/operations/deployment-guide.md`
- Public API: `../../docs/api/external.md`
- Internal/admin/relay API: `../../docs/api/internal.md`
- GitHub Actions CI/CD: `../../docs/operations/ci-cd.md`

Generate production secrets with `../scripts/generate-control-plane-env.sh`. Production CD sets `DEPLOY_SOURCE=ci` and supplies an immutable `ghcr.io/...:sha-<commit>` through `CONTROL_PLANE_IMAGE` or `EDGE_RELAY_IMAGE`; the scripts then pull the CI artifact instead of rebuilding on the host. Use `DEPLOY_SOURCE=source` only for an intentional local source build. Concrete `.env`, edge YAML, `identity.json`, and backups are intentionally ignored by Git.
