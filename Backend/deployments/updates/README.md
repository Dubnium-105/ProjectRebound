# Signed update release descriptors

English | [简体中文](README.zh-CN.md)

Place one non-secret JSON descriptor per release in this directory. At control-plane startup, every `*.json` file is strictly decoded, validated, sorted by file path, assigned immutable CDN URLs, and signed with Ed25519.

The signing private key is never stored here. Supply a 32-byte Ed25519 seed or 64-byte private key through `UPDATE_SIGNING_PRIVATE_KEY_BASE64`. Production startup fails when the key or all release descriptors are missing.

Example descriptor:

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

`manifest_hash` is SHA-256 over deterministic JSON that excludes both `manifest_hash` and `signature`. The Ed25519 signature is then calculated over deterministic JSON containing `manifest_hash` but excluding only `signature`. Clients select an embedded public key by `key_id`, verify the signature before downloading, and verify each downloaded file's exact size and SHA-256 before installation.
