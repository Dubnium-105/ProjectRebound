# 下载目录存储

[English](download-catalog.md) | 简体中文

通用下载目录与启动器更新 Manifest 相互独立，默认关闭。设置
`DOWNLOADS_ENABLED=true` 前，先创建 S3 兼容桶和公开 CDN 域名。写入凭据只授予
服务端生成对象前缀所需的最小权限，且不得写入 YAML 或日志。

必填环境变量为 `DOWNLOAD_S3_ENDPOINT`、`DOWNLOAD_S3_REGION`、
`DOWNLOAD_S3_BUCKET`、`DOWNLOAD_S3_ACCESS_KEY_ID`、
`DOWNLOAD_S3_SECRET_ACCESS_KEY` 和 `DOWNLOAD_PUBLIC_BASE_URL`。R2 通常使用
`auto` region，AWS S3 使用真实 region；MinIO endpoint 必须同时能被控制面和管理员
浏览器访问。公开基址应把相同对象 key 映射到不可变对象，并支持 `HEAD`、`GET` 和 Range。

## 桶 CORS

浏览器通过预签名 S3 URL 直传。桶 CORS 只允许实际 Admin Web 来源，允许 `PUT`、
`HEAD`，允许全部签名/内容请求头，并暴露 `ETag`。使用凭据时不得配通配来源。R2
配置说明见 [Cloudflare R2 CORS 文档](https://developers.cloudflare.com/r2/buckets/cors/)；
风格最小配置如下：

```json
[
  {
    "AllowedOrigins": ["https://admin.project-rebound.space"],
    "AllowedMethods": ["PUT", "HEAD"],
    "AllowedHeaders": ["*"],
    "ExposeHeaders": ["ETag"],
    "MaxAgeSeconds": 3600
  }
]
```

## 生命周期与恢复

超过 64 MiB 的文件使用 16 MiB 分片，浏览器最多四路并发；会话 24 小时后过期，
后台任务会中止过期 multipart。浏览器可恢复已上传分片，小文件的单次 PUT 也能续签。
完成上传后，后台任务会流式复算对象大小与 SHA-256，通过后才能发布。

单文件 PUT 带 `If-None-Match: *`，因此尚未过期的签名也不能覆盖首次成功创建的对象；
multipart 完成后，原 upload ID 不能再接收分片或重复完成。不要给管理员或无关服务
授予该前缀的无条件覆盖权限；若所选供应商支持，可再启用版本控制或 Object Lock。
底层前置条件语义见 [Amazon S3 条件写入文档](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html)。

发布和归档分别要求 `downloads.publish`、`downloads.archive`，并要求操作原因和当前
会话 MFA Step-up。归档后的数据库记录与对象永久保留；确认桶生命周期规则不会删除
该前缀，并把下载表和存储凭据纳入备份、轮换流程。审计仅记录 ID 与状态变化，严禁
记录凭据、预签名 URL、ETag 或上传内容。

PostgreSQL 生命周期集成测试需要设置 `TEST_DATABASE_URL`。如需针对 MinIO/R2/S3
执行真实的单文件与 multipart 对象测试，还要设置 `TEST_DOWNLOAD_S3_ENDPOINT`、
`TEST_DOWNLOAD_S3_BUCKET`、`TEST_DOWNLOAD_S3_ACCESS_KEY_ID`、
`TEST_DOWNLOAD_S3_SECRET_ACCESS_KEY`，并可选设置 `TEST_DOWNLOAD_S3_REGION`、
`TEST_DOWNLOAD_S3_PUBLIC_BASE_URL`，然后运行 `go test ./internal/download -count=1`。
请使用空的专用测试桶；测试只会创建并清理 `downloads/integration/` 下的对象。
