# BoundaryMetaServer 协议来源

[English](UPSTREAM.md) | 简体中文

- 仓库：`https://github.com/Dubnium-105/BoundaryMetaServer`
- 分支：`master`
- Commit：`d68e717267abf14e32d4e39618f9b7680ed93046`
- 导入目录：`game/proto/Request`、`game/proto/Response`
- 导入文件数：41
- 逐文件哈希：`UPSTREAM_MANIFEST.sha256`
- 许可证：GNU Affero General Public License v3.0，与 Project Rebound 一致。

`upstream/` 保存用于协议审查和漂移检测的源码镜像。生产代码使用由
`metaserver.proto` 生成的静态 Go 消息，不会在运行时解析 `.proto` 文件。

`Response/matchmaking_ext.proto` 将部分字段编号标记为 tentative。在完成脱敏的真实
客户端抓包审查并提交 golden packet 之前，这些字段不会进入生产匹配状态机。
