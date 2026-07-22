# 项目反弹监测

[English](README.md) | 简体中文


提供的 Prometheus 配置在内部抓取控制平面，并可选择从以下位置发现直接边缘中继目标：`targets/edge-relays.yml`。控制平面已导出库存、生命周期、容量、mTLS 连接和中继`TrafficReport`每个注册节点的遥测，因此新添加的中继不需要直接抓取。当未配置节点本地指标传输时，请将发现文件保留为空。直接目标对于故障排除仍然有用，并且应该稳定`instance`, `node_id`, `region`， 和`environment`标签。

Edge Relay 进程故意将其指标侦听器保持在环回状态。不要改变`metrics_addr`到公共地址。运行节点本地代理或监控代理，读取`127.0.0.1:<metrics-port>`并且仅通过经过身份验证的专用网络（例如 Tailscale 或 WireGuard）公开它。 Prometheus 永远不应该通过公共接口抓取中继指标。

Grafana 配置目录安装`project-rebound-prometheus`数据源、动态**项目反弹操作**队列视图以及八个 V1.1 深入分析仪表板：控制平面概述、身份验证和会话安全、P2P 房间和连接、中继队列概述、中继安全、中继流量和容量、数据库和 Redis 以及发布和更新状态。操作仪表板涵盖：

- 控制平面和每个在线中继的重复服务目标卡，以及 mTLS 控制连接在线的每个中继的详细状态卡；当在线节点集发生变化时，Grafana 会自动添加并包装两个卡组，同时库存表继续显示每个注册的节点，包括离线和撤销的节点；
- 控制平面 HTTP 流量、P95 延迟、会话、房间、分配、安全故障、数据库池和中继注册表状态；
- 边缘中继控制连接、重新连接、分配、数据包转发/丢弃、流量、无效令牌和速率限制；
- 当节点导出器作业使用时，控制平面和中继主机 CPU、内存、根文件系统和网络吞吐量`project-rebound-node.*`命名约定。

普罗米修斯负载`alerts/v1.1.rules.yml`。它涵盖 API 可用性/延迟、PostgreSQL 和 Redis、池和磁盘压力、身份验证滥用、中继可用性/安全性/容量/迁移以及备份新鲜度。将警报路由到生产中操作员的警报管理器；存储库故意不包含通知凭据。

备份脚本可以通过设置发布文本文件收集器指标`BACKUP_METRICS_DIRECTORY`到节点导出器文本文件目录（通常`/var/lib/node_exporter/textfile_collector`）。配置节点导出器`--collector.textfile.directory`对于同一目录。备份和恢复脚本自动独立更新`.prom`文件，因此失败的运行不会删除先前成功备份的时间戳。

对于单独的 Prometheus/Grafana 安装，请将仪表板和配置文件复制到该堆栈中，将数据源指向其 Prometheus URL，然后添加以下作业：

- `project-rebound-control-plane`和`/internal/metrics`在私有控制平面监听器上；
- `project-rebound-edge-relay`通过节点本地私有度量传输；
- `project-rebound-node-control-plane`和`project-rebound-node-edge-relay`用于主机资源指标。

重新加载之前验证更改：

```bash
promtool check config /etc/prometheus/prometheus.yml
promtool check rules /etc/prometheus/alerts/v1.1.rules.yml
curl -fsS http://127.0.0.1:9090/api/v1/targets
curl -fsS http://127.0.0.1:9090/api/v1/rules
curl -fsS http://127.0.0.1:3000/api/health
```
