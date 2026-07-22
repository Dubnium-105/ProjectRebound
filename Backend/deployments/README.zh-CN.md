# 部署入口点

[English](README.md) | 简体中文


- 本地一体化开发：`compose/docker-compose.yaml`
- 分离的控制平面：`control-plane/docker-compose.yaml`
- 分离的Linux边缘中继：`edge-relay/docker-compose.yaml`
- 普罗米修斯和格拉法纳：`monitoring/README.md`
- 完整的 Debian 程序：`../../docs/operations/deployment-guide.md`
- 公共API：`../../docs/api/external.md`
- 内部/管理/中继 API：`../../docs/api/internal.md`
- GitHub Actions CI/CD：`../../docs/operations/ci-cd.md`

生成生产机密`../scripts/generate-control-plane-env.sh`。制作CD套`DEPLOY_SOURCE=ci`并提供一个不可变的`ghcr.io/...:sha-<commit>`通过`CONTROL_PLANE_IMAGE`或者`EDGE_RELAY_IMAGE`;然后，脚本会拉取 CI 工件，而不是在主机上重建。使用`DEPLOY_SOURCE=source`仅用于有意的本地源构建。具体的`.env`, 边缘 YAML,`identity.json`，并且 Git 故意忽略备份。
