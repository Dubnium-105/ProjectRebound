# ProjectRebound CI/CD 使用与配置

[English](ci-cd.md) | 简体中文

本仓库使用 GitHub Actions、GitHub Container Registry（GHCR）、GitHub Environments 和原生 OpenSSH 完成 CI/CD。控制面、MetaServer 和 Edge Relay 是独立部署目标，可以使用不同主机、审批人、回滚动作和 Secrets。

## 1. 工作流

### CI and Images

文件：`.github/workflows/ci.yml`

每次 push、针对 main 的 pull request 和手动运行都会执行：

1. Go 格式检查和 `go mod verify`；
2. `go vet ./...`；
3. PostgreSQL 17 service container 上的 `go test -race ./...`；
4. Control Plane、MetaServer、Meta 导入工具、Windows MetaTunnel 和 Edge Relay 二进制构建；
5. actionlint、Shell 语法和 LF 行尾检查；
6. 密钥生成器、两份 Compose 和 Caddy 配置校验；
7. Meta protobuf 生成/漂移、definitions、确定次数的分帧 fuzz、race、Gate 重放/IDOR、调度/QoS、`govulncheck`、镜像漏洞、SBOM 和 provenance 门禁；
8. 所有质量检查通过后，各使用一次 Buildx 构建控制面、MetaServer 和 Edge Relay 镜像。

普通分支、`main`、`v*` tag 的 push，以及手动运行 CI，都会把这次构建直接发布为可部署的 GHCR CI 产物：

```text
ghcr.io/<owner>/projectrebound-control-plane:sha-<40-char-commit>
ghcr.io/<owner>/projectrebound-meta-server:sha-<40-char-commit>
ghcr.io/<owner>/projectrebound-edge-relay:sha-<40-char-commit>
```

Pull request 只构建和验证，不登录 GHCR，也不发布镜像。`main` 同时更新 `:main`，tag push 同时发布对应 tag。部署始终使用完整 SHA tag，不使用可移动的 `main` 或 `latest`。镜像附带 OCI metadata 和 GitHub artifact provenance attestation。

容器不会先在一个 job 构建、再在发布 job 重复构建。质量检查通过后，同一个矩阵 job 构建一次并直接推送不可变 SHA 镜像；CD 读取 CI 的 commit SHA，在远端设置 `DEPLOY_SOURCE=ci` 并只执行 `docker compose pull`，不会在部署主机重新编译源码。

### Deploy

文件：`.github/workflows/deploy.yml`

- main 的 CI 和镜像发布全部成功后，如果仓库变量 `ENABLE_STAGING_DEPLOY=true`，自动部署三个 staging target。
- `workflow_dispatch` 可以手动选择 `staging`/`production` 和 `control-plane`/`meta-server`/`edge-relay`/`all`。
- 对 `all` 和自动 staging，MetaServer 会等待控制面部署完成后再操作共享 Compose project；仅部署 MetaServer 时，控制面 job 跳过后仍可正常执行。
- production 应通过 GitHub Environment Required Reviewers 审批，并启用 Prevent self-review。
- 同一环境和 target 的部署使用 concurrency 串行化，不会互相取消。
- 部署前控制面自动创建 PostgreSQL custom-format 备份。
- 部署完成后执行健康检查；失败时自动尝试上一 release 和上一镜像。

## 2. GitHub 仓库设置

### 2.1 Actions 权限

在 `Settings -> Actions -> General` 保留最小默认权限。工作流顶层只有 `contents: read`；仅容器构建/发布 job 临时获得：

```yaml
packages: write
attestations: write
id-token: write
```

不要创建拥有仓库管理权限的 PAT 供镜像发布；发布 job 使用 GitHub 自动生成的 `GITHUB_TOKEN`。

### 2.2 Environments

创建八个 Environment：

```text
staging-control-plane
staging-meta-server
staging-edge-relay-gateway
staging-edge-relay-hgh
production-control-plane
production-meta-server
production-edge-relay-gateway
production-edge-relay-hgh
```

production 环境建议配置：

- Required reviewers；
- Prevent self-review；
- 只允许 main 和受保护的 `v*` tag；
- 禁止管理员绕过保护规则；
- Secrets 仅放在对应 Environment，不放在仓库级别。

### 2.3 仓库级变量

| 名称 | 示例 | 说明 |
| --- | --- | --- |
| `ENABLE_STAGING_DEPLOY` | `false` | 设为 `true` 才启用 main 自动部署 staging |

首次配置完成并手动部署成功之前保持 `false`。

## 3. 每个 Environment 的配置

所有目标都设置以下 Variables：

| Variable | 示例 | 说明 |
| --- | --- | --- |
| `DEPLOY_HOST` | `192.0.2.10` | SSH DNS 名或 IPv4；工作流故意不接受未规范化输入 |
| `DEPLOY_PORT` | `22` | SSH 端口 |
| `DEPLOY_USER` | `projectrebound` | 无交互部署用户 |
| `DEPLOY_ROOT` | `/opt/projectrebound-control` | release、current symlink 和备份根目录 |

Control Plane Environment 额外 Variables：

| Variable | 示例 |
| --- | --- |
| `CONTROL_PLANE_ENV_FILE` | `/etc/projectrebound/control-plane.env` |
| `PUBLIC_BASE_URL` | `https://api.example.com` |
| `ENABLE_MONITORING` | `1` |

MetaServer Environment 额外 Variables：

| Variable | 示例 |
| --- | --- |
| `CONTROL_PLANE_ENV_FILE` | `/etc/projectrebound/control-plane.env` |
| `META_PUBLIC_BASE_URL` | `https://meta.dubnium.top` |

MetaServer Environment 可以指向同一控制面主机，但审批、concurrency 和回滚仍独立。
Meta 部署失败只回滚自身镜像，不重启 control-plane 服务。

Edge Relay Environment 额外 Variables：

| Variable | 示例 |
| --- | --- |
| `EDGE_RELAY_ENV_FILE` | `/etc/projectrebound/edge-relay.env` |
| `EDGE_RELAY_CONFIG_FILE` | `/etc/projectrebound/config.edge-relay.yaml` |

每个 Environment 添加以下 Secrets：

| Secret | 内容 |
| --- | --- |
| `SSH_PRIVATE_KEY` | 该目标专用的无口令 Ed25519 deploy key 私钥 |
| `SSH_KNOWN_HOSTS` | 通过可信带外渠道核对过的目标 host key 行 |

不要在工作流中运行未经核验的 `ssh-keyscan`。`SSH_KNOWN_HOSTS` 必须从控制台、云厂商指纹或另一可信通道核对。

工作流仅授予短期 `GITHUB_TOKEN` `contents: read` 与 `packages: read`
权限，并用该令牌完成远端 GHCR 登录。不要为部署创建或保存长期 Package
PAT。

两个 Edge Relay Environment 分别拥有独立的部署、审批、并发、凭据撤销与
回滚边界。部署 `edge-relay` 时通过 `fail-fast: false` 同时覆盖两个节点，
单节点失败不会取消另一节点的执行结果。

## 4. 远端主机首次准备

安装 Docker 和依赖的完整步骤见 `docs/operations/deployment-guide.md`。为每个目标创建独立用户和目录，例如：

```bash
sudo useradd --create-home --shell /bin/bash projectrebound
sudo install -d -o projectrebound -g projectrebound \
  /opt/projectrebound-control \
  /opt/projectrebound-control/releases \
  /opt/projectrebound-control/backups
sudo install -d -m 700 -o projectrebound -g projectrebound /etc/projectrebound
```

把部署公钥加入 `/home/projectrebound/.ssh/authorized_keys`。推荐仅允许来源于 GitHub-hosted runner 出口经过的 VPN/bastion；公网直接开放 SSH 时必须额外使用防火墙、Fail2ban 和严格的 key-only 登录。

控制面主机生成并编辑持久配置：

```bash
cd /path/to/checked-out/Backend
./scripts/generate-control-plane-env.sh /etc/projectrebound/control-plane.env
chmod 600 /etc/projectrebound/control-plane.env
```

Edge 主机准备：

```bash
install -m 600 deployments/edge-relay/.env.example \
  /etc/projectrebound/edge-relay.env
install -m 600 deployments/edge-relay/config.edge-relay.yaml.example \
  /etc/projectrebound/config.edge-relay.yaml
```

编辑所有 placeholder。首次 Edge 注册时设置 Bootstrap Token；部署脚本成功后会清空 Edge env 中的 token，并通过重建验证 identity volume。

部署用户必须能执行 Docker。可以加入 `docker` group，也可以像测试环境一样仅授予无密码 `sudo docker`；后者仍接近 root 权限，应限制 SSH key 和 sudoers command scope。

## 5. 首次发布和部署

1. push 到准备部署的分支（首次生产发布通常合并到 main），等待 `CI and Images` 全绿。
2. 在仓库 Packages 页面确认三个 `sha-<commit>` 镜像和 Windows MetaTunnel artifact 存在。
3. 保持 `ENABLE_STAGING_DEPLOY=false`。
4. 打开 `Actions -> Deploy -> Run workflow`。
5. 选择 `staging` 和一个 target；`commit_sha` 留空表示当前所选 ref。
6. 分别验证 control-plane、MetaServer 和 edge-relay。
7. 再选择 `all` 做一次完整 staging 发布。
8. 验证成功后可把 `ENABLE_STAGING_DEPLOY` 设为 `true`。

Deploy 工作流是生产推荐入口。确需在目标机直接运行底层脚本时，先登录 GHCR，再明确指定 CI 产物：

```bash
DEPLOY_SOURCE=ci \
CONTROL_PLANE_IMAGE=ghcr.io/<owner>/projectrebound-control-plane:sha-<40-char-commit> \
  ./scripts/deploy-control-plane.sh

DEPLOY_SOURCE=ci \
META_SERVER_IMAGE=ghcr.io/<owner>/projectrebound-meta-server:sha-<40-char-commit> \
  ./scripts/deploy-meta-server.sh

DEPLOY_SOURCE=ci \
EDGE_RELAY_IMAGE=ghcr.io/<owner>/projectrebound-edge-relay:sha-<40-char-commit> \
  ./scripts/deploy-edge-relay.sh
```

脚本默认 `DEPLOY_SOURCE=auto`：合法 GHCR SHA 镜像自动走 CI 拉取，否则回退到源码构建。生产自动化始终显式使用 `ci`，从而阻止镜像变量缺失时意外在服务器现场构建。

生产发布建议先创建并推送受保护 tag：

```bash
git tag -s v1.0.0 -m "ProjectRebound v1.0.0"
git push origin v1.0.0
```

等待 tag 镜像发布成功，然后手动运行 Deploy，环境选择 `production`，填写该 tag 对应的完整 commit SHA。审批人核对 CI、镜像 digest、数据库备份和变更单后批准。

## 6. 远端 release 布局

```text
/opt/projectrebound-control/
  releases/
    sha-<commit>-<run>-<attempt>-control/
      Backend/
      .deployed-image
  current-control-plane -> releases/<active-release>
  current-meta-server -> releases/<active-meta-release>
  backups/
```

Edge 使用 `current-edge-relay`。MetaServer 的 release pointer 和回滚镜像与
control-plane 相互独立。部署 bundle 不包含 `.env`、具体 Edge YAML、
`identity.json` 或备份。GHCR Token 只通过 SSH stdin 发送给
`docker login --password-stdin`，不会出现在远端命令参数或 bundle 中。

工作流不会自动删除旧 release。确认备份和回滚窗口后，由运维人员按明确路径清理。

## 7. 回滚

部署失败时 `remote-deploy.sh` 自动读取上一 release 的 `.deployed-image` 并尝试恢复。自动回滚也失败时，工作流会明确报告 `ROLLBACK_FAILED`，需要人工处理。

主动回滚：手动运行 Deploy，把 `commit_sha` 设置为仍存在于 GHCR 的旧完整 SHA。控制面在切换前仍会备份数据库。应用回滚不能自动撤销数据库迁移；不向后兼容的数据库恢复必须按照生产事故流程执行。

## 8. 分支保护建议

main 至少要求以下 checks：

```text
Go backend and PostgreSQL
Deployment and workflow configuration
Build and package control-plane image
Build and package meta-server image
Build and package edge-relay image
```

同时启用：

- Require pull request reviews；
- Require branches to be up to date；
- Require conversation resolution；
- 禁止 force push 和 branch deletion；
- 限制可以创建 `v*` tag 的人员。

Dependabot 每周检查 GitHub Actions major tag 更新。安全要求更高时，应把所有第三方 Action 从 major tag 固定到完整 commit SHA，并通过受控 Dependabot PR 更新。

## 9. 故障定位

- CI Go 失败：先看 PostgreSQL service health，再看具体 package 输出。
- Compose/Caddy 失败：运行 `Backend/scripts/generate-control-plane-env.sh` 后复现 workflow 中的 config 命令。
- GHCR push 403：检查 job 的 `packages: write` 和 package/repository 关联。
- SSH host verification failed：重新通过可信通道核对 host key，不要禁用 `StrictHostKeyChecking`。
- Remote pull denied：检查 Environment 中 GHCR 账号是否有 `read:packages`。
- Control deploy failed：查看 workflow 的备份结果、健康检查和 `ROLLBACK_OK/FAILED`。
- Meta deploy failed：检查 28 号迁移 readiness、受限 PostgreSQL/Redis provisioning 及 Logic/HTTP 端口；回滚不得重启 control-plane。
- Edge deploy failed：检查 443 enrollment、9090 mTLS 和 UDP advertised endpoint；不要删除 identity volume 作为第一反应。

GitHub 官方参考：

- https://docs.github.com/actions/tutorials/publish-packages/publish-docker-images
- https://docs.github.com/actions/reference/workflows-and-actions/deployments-and-environments
- https://docs.github.com/actions/reference/security/secure-use
