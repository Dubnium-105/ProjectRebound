[English](README.md) | 简体中文

# 实机测试包构建

本目录为尚未完成原生验收的严格名单候选准备测试分发材料。它不修改生产发布门禁，也不把包构建成功作为在线对局验收通过。

`build_test_packages.py` 接收已经审阅的 Windows staging 目录和公开元数据 JSON；如需交接服务端产物，可另传 `--server-stage`。每个二进制都必须在 `required_artifacts` 中显式列出路径、字节数与 SHA-256。脚本拒绝私钥、数据库转储、凭据配置和额外二进制，为每个包生成逐文件清单，并重新读取 ZIP/tar.gz 核对文件集合与原始字节。

```text
python build_test_packages.py --windows-stage <windows-staging> \
  --metadata <public-metadata.json> \
  --output <new-output-directory>
```

输出目录必须不存在。Windows 包需要由测试者自行安装合法的固定版本 Boundary 游戏；不分发游戏 EXE、Steam 会话或开发机配置。本轮直接使用现有 api/cnapi/meta 服务，普通客户端不编入 lab-testing，也不另设测试后端。若交接服务端产物，它仅供协调员更新现有服务，不能作为普通客户端安装包使用。包中不包含既有账号、私钥或访问凭据。

元数据必须包含 `schema_version=1`、`purpose=hardware-test-only`、`release_ready=false`、`package_id`、实际 `source_commits`、`pinned_game`，以及分别按 `windows-x64`、`server-linux-amd64` 列出的 `required_artifacts`。每个包记录角色与所有普通文件的哈希；`package-manifest.json` 自身由 `SHA256SUMS` 覆盖，后者由外部 ZIP/tar.gz 哈希覆盖。

安装和恢复脚本在 `windows-install` 目录，检查和收集脚本在 `windows-native` 目录。`sign_test_artifact.ps1` 使用已有代码签名证书对新副本签名，核对与产品相同的 WinVerifyTrust 结果，不导出私钥或修改信任存储。必须先签 Payload，再以签后 SHA 编译严格 Toolbox，最后签 Toolbox。实际构建命令、源提交、执行结果与分发文件摘要应写入该次测试构建记录，历史验收台账保持原样。
