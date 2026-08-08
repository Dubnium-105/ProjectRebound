# 下载目录与本机 MinIO

[English](download-catalog.md) | 简体中文

通用下载目录与启动器更新 Manifest 相互独立。Control Plane 的生产和开发 Compose
默认启动本机 MinIO，并令管理台下载功能默认启用；直接运行二进制时仍保持显式配置，
避免在缺少凭据时误启用。

## 默认拓扑

生产部署使用三个入口：

- `MINIO_S3_SITE`：MinIO S3 API 域名，供控制面和管理员浏览器使用预签名 PUT。
- `DOWNLOADS_SITE`：只允许 `GET`、`HEAD`，且只代理
  `/<bucket>/downloads/*` 的公开下载域名。
- `127.0.0.1:MINIO_CONSOLE_PORT`：仅通过 SSH 隧道访问的 MinIO Console。

为 `MINIO_S3_SITE` 和 `DOWNLOADS_SITE` 配置指向 Control Plane 主机的 DNS。Caddy
自动申请 TLS，并在内部网络为这两个域名提供别名，避免控制面依赖公网回环路由。
生产环境不得直接暴露 MinIO 的 9000/9001 端口。

运行 `scripts/generate-control-plane-env.sh` 会生成彼此独立的 MinIO root 凭据和
`DOWNLOAD_S3_*` 应用凭据。部署前把 `.env` 中的 `admin.example.com`、
`s3.example.com`、`downloads.example.com` 替换为真实域名，并保持：

```dotenv
DOWNLOADS_ENABLED=true
DOWNLOAD_S3_ENDPOINT=https://s3.example.com
DOWNLOAD_S3_REGION=us-east-1
DOWNLOAD_S3_BUCKET=project-rebound-downloads
DOWNLOAD_PUBLIC_BASE_URL=https://downloads.example.com/project-rebound-downloads
MINIO_CORS_ALLOWED_ORIGINS=https://admin.example.com
```

公开基址必须包含桶名，因为服务随后直接追加服务端生成的
`downloads/<item-slug>/<version-id>/<filename>` 对象 key。

## 自动初始化

`minio-provision` 是可重复运行的一次性容器。每次部署时它会：

1. 创建 `DOWNLOAD_S3_BUCKET`；
2. 创建/更新独立的控制面应用用户；
3. 仅授予 `downloads/*` 所需的 PUT、GET、DELETE 和 multipart 权限；
4. 为管理台来源配置桶级 PUT/HEAD CORS，并暴露 `ETag`；
5. 只为 `downloads/` 前缀授予匿名 `GetObject`，明确不授予桶列表权限。

MinIO root 凭据只进入 MinIO 与初始化容器，不会进入 Control Plane 或 Admin Web。
管理台 CSP 只允许连接 `MINIO_S3_SITE`，公开下载域名不会接受上传和管理请求。

MinIO CORS 管理说明见
[MinIO CORS 文档](https://docs.min.io/aistor/administration/cors-configuration/)。

## 运维

MinIO Console 默认绑定回环地址，可用以下方式访问：

```bash
ssh -L 9001:127.0.0.1:9001 user@CONTROL_HOST
```

然后打开 `http://127.0.0.1:9001`。不要使用 root 凭据作为应用凭据，也不要把
`.env`、预签名 URL 或 ETag 写入日志和工单。

对象保存在 `minio-data` volume。单节点 volume 不是备份：生产环境应使用本地直连
冗余磁盘或分布式 MinIO，并定期制作异机备份。不得给 `downloads/` 配置自动删除
生命周期规则；归档后的数据库记录和对象需要永久保留。更换 MinIO 服务地址时，
必须同时更新 S3 API 域名、公开基址、管理台 CSP 和桶 CORS。

超过 64 MiB 的文件默认使用 16 MiB 分片，浏览器最多四路并发；会话 24 小时后
过期。上传完成后，后台任务从 MinIO 流式复算大小与 SHA-256，校验通过后才能发布。

## 验证

启动默认部署：

```bash
docker compose --env-file deployments/control-plane/.env \
  -f deployments/control-plane/docker-compose.yaml up -d
```

检查 MinIO 初始化和 Control Plane：

```bash
docker compose --env-file deployments/control-plane/.env \
  -f deployments/control-plane/docker-compose.yaml logs minio-provision control-plane
curl -fsS https://api.example.com/v1/downloads
```

真实对象集成测试应使用专用测试桶，并设置 `TEST_DOWNLOAD_S3_ENDPOINT`、
`TEST_DOWNLOAD_S3_BUCKET`、`TEST_DOWNLOAD_S3_ACCESS_KEY_ID`、
`TEST_DOWNLOAD_S3_SECRET_ACCESS_KEY`；可选设置 `TEST_DOWNLOAD_S3_REGION` 和
`TEST_DOWNLOAD_S3_PUBLIC_BASE_URL`。运行：

```bash
go test ./internal/download -run TestS3StorageAgainstCompatibleService -count=1
```

测试只会创建并清理 `downloads/integration/` 下的对象。
