# Deployment entry points

- Local all-in-one development: `compose/docker-compose.yaml`
- Separated control plane: `control-plane/docker-compose.yaml`
- Separated Linux edge relay: `edge-relay/docker-compose.yaml`
- Full Debian procedure: `../../docs/debian-deployment-and-ops.md`
- Public API: `../../docs/control-plane-external-api.md`
- Internal/admin/relay API: `../../docs/control-plane-internal-api.md`
- GitHub Actions CI/CD: `../../docs/cicd.md`

Generate production secrets with `../scripts/generate-control-plane-env.sh`, deploy the control plane with `../scripts/deploy-control-plane.sh`, and deploy each edge host with `../scripts/deploy-edge-relay.sh`. Concrete `.env`, edge YAML, `identity.json`, and backups are intentionally ignored by Git.
