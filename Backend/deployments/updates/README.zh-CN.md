# 签名的更新发布描述符

[English](README.md) | 简体中文


每个版本在此目录中放置一个非秘密 JSON 描述符。在控制平面启动时，每个`*.json`文件经过严格解码、验证、按文件路径排序、分配不可变的 CDN URL 并使用 Ed25519 进行签名。

签名私钥永远不会存储在这里。通过提供 32 字节 Ed25519 种子或 64 字节私钥`UPDATE_SIGNING_PRIVATE_KEY_BASE64`。当密钥或所有发布描述符丢失时，生产启动失败。

描述符示例：

```json
{
  "schema_version": 1,
  "product": "project-rebound",
  "platform": "windows",
  "architecture": "amd64",
  "channel": "stable",
  "version": "1.2.0",
  "minimum_supported_version": "1.1.0",
  "published_at": "2026-07-18T00:00:00Z",
  "files": [
    {
      "file_id": "windows_amd64_1_2_0_game",
      "path": "ProjectRebound.exe",
      "size": 12345678,
      "sha256": "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
      "compression": "none",
      "object_key": "stable/windows/amd64/1.2.0/ProjectRebound.exe"
    }
  ]
}
```

`manifest_hash`SHA-256 优于确定性 JSON，排除两者`manifest_hash`和`signature`。然后通过确定性 JSON 计算 Ed25519 签名，其中包含`manifest_hash`但仅排除`signature`。客户端通过以下方式选择嵌入的公钥`key_id`，下载前验证签名，并在安装前验证每个下载文件的确切大小和 SHA-256。
