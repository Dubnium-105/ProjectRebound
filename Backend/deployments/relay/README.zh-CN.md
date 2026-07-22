# 边缘中继运行时间

[English](README.md) | 简体中文


对于在单独的 Linux 主机上运行的中继，请使用`../edge-relay/docker-compose.yaml`以及完整的程序`../../../docs/operations/deployment-guide.md`。该目录包含图像定义和协议运行时资产；开发 Compose 文件中的可选中继配置文件仍然仅用于本地集成。

该映像包含一个静态链接的 Go 二进制文件以及 HTTPS 注册所需的系统 CA 捆绑包。它不连接到 PostgreSQL、Redis、NATS 或游戏服务。持久本地状态仅限于节点的私钥、mTLS 证书、不透明节点凭证、CA 和中继令牌公钥集`identity.json`.

山`config.edge-relay.yaml`只读于`/etc/projectrebound/config.edge-relay.yaml`并在配置的位置挂载节点本地可写目录`data_dir`。提供一次性`EDGE_RELAY_BOOTSTRAP_TOKEN`仅适用于首次注册，然后将其从部署机密集中删除。

将 UDP 8443 公开为所需的外部 UDP 端口（通常为 443）。保持指标侦听器绑定到环回。允许出站 HTTPS 到注册端点，并允许出站 mTLS gRPC 到配置的控制地址。

`scripts/deploy-edge-relay.sh`更喜欢 Docker Compose v2，支持独立版`docker-compose`命令，并回退到等效的隔离`docker run`未安装 Compose 实现时的部署。放`EDGE_RELAY_RUNTIME=compose`或者`EDGE_RELAY_RUNTIME=raw-docker`明确要求一种模式。 Raw Docker 模式使用稳定的容器名称`project-rebound-edge-relay`和持久量`project-rebound-edge-relay-data`;两者都可以被覆盖`EDGE_RELAY_CONTAINER_NAME`和`EDGE_RELAY_VOLUME_NAME`.
