import {
  ApiOutlined,
  AuditOutlined,
  CheckCircleFilled,
  ClockCircleOutlined,
  DatabaseOutlined,
  KeyOutlined,
  LockOutlined,
  SafetyCertificateOutlined,
  TeamOutlined
} from "@ant-design/icons";
import {Alert, Card, Col, Progress, Row, Segmented, Skeleton, Space, Statistic, Tag, Timeline, Typography} from "antd";
import {useEffect, useState} from "react";
import {Link} from "react-router";
import {ApiError, apiRequest} from "../api/client";
import type {DashboardAlert, DashboardPoint, DashboardSummary} from "../types";

const modules = [
  {
    icon: <TeamOutlined />,
    title: "玩家与会话",
    description: "查询玩家、变更账号状态、撤销登录会话",
    href: "/players",
    status: "已接入"
  },
  {
    icon: <KeyOutlined />,
    title: "邀请码",
    description: "查看邀请码批次、使用情况与有效期",
    href: "/invite-codes",
    status: "已接入"
  },
  {
    icon: <SafetyCertificateOutlined />,
    title: "登录风险",
    description: "查看异常认证事件及关联请求",
    href: "/risk-events",
    status: "已接入"
  },
  {
    icon: <ApiOutlined />,
    title: "联机资源",
    description: "房间、专服、中继节点与活动连接",
    href: "/online/connections",
    status: "已接入"
  }
];

export function DashboardPage() {
  const [summary, setSummary] = useState<DashboardSummary | null>(null);
  const [alerts, setAlerts] = useState<DashboardAlert[]>([]);
  const [points, setPoints] = useState<DashboardPoint[]>([]);
  const [period, setPeriod] = useState<"1h" | "24h" | "7d" | "30d">("24h");
  const [loadError, setLoadError] = useState("");

  useEffect(() => {
    let active = true;
    const load = () => {
      Promise.all([
        apiRequest<DashboardSummary>("/v1/admin/dashboard/summary"),
        apiRequest<{items: DashboardAlert[]}>("/v1/admin/dashboard/alerts"),
        apiRequest<{items: DashboardPoint[]}>(
          `/v1/admin/dashboard/timeseries?period=${encodeURIComponent(period)}`
        )
      ])
        .then(([nextSummary, nextAlerts, nextPoints]) => {
          if (active) {
            setSummary(nextSummary);
            setAlerts(nextAlerts.items);
            setPoints(nextPoints.items);
            setLoadError("");
          }
        })
        .catch((error) => {
          if (active) {
            setLoadError(error instanceof ApiError ? error.message : "无法读取运营指标。");
          }
        });
    };
    load();
    const interval = window.setInterval(load, 30_000);
    return () => {
      active = false;
      window.clearInterval(interval);
    };
  }, [period]);

  return (
    <div className="page-stack">
      <section className="page-hero compact">
        <div>
          <Typography.Text className="eyebrow">CONTROL PLANE / OVERVIEW</Typography.Text>
          <Typography.Title level={1}>运营总览</Typography.Title>
          <Typography.Paragraph>
            管理入口已经与玩家认证分离。当前控制台只展示控制面明确授权的数据与操作，不直接连接数据库或中继节点。
          </Typography.Paragraph>
        </div>
        <div className="hero-status">
          <div className="pulse-dot" />
          <div>
            <strong>安全会话有效</strong>
            <span>短期 Access Token · HttpOnly Refresh Cookie</span>
          </div>
        </div>
      </section>

      {loadError && (
        <Alert
          type="error"
          showIcon
          title="运营指标暂时不可用"
          description={`${loadError} 请稍后刷新；若持续失败，请将页面中的请求编号提供给平台值班人员。`}
        />
      )}
      <Row gutter={[16, 16]}>
        {[
          ["当前在线玩家", summary?.online_players],
          ["活动 P2P 房间", summary?.active_p2p_rooms],
          ["在线 Dedicated Server", summary?.online_game_servers],
          ["READY 中继节点", summary?.ready_relay_nodes],
          ["活动中继分配", summary?.active_relay_allocations],
          ["待处理风险事件", summary?.unresolved_risk_events]
        ].map(([title, value]) => (
          <Col xs={12} md={8} xl={4} key={String(title)}>
            <Card className="metric-card">
              {summary ? (
                <Statistic title={title} value={value as number} />
              ) : (
                <Skeleton active paragraph={false} />
              )}
            </Card>
          </Col>
        ))}
      </Row>

      <Card
        title="运营趋势"
        extra={
          <Segmented
            size="small"
            value={period}
            options={[
              {label: "1 小时", value: "1h"},
              {label: "24 小时", value: "24h"},
              {label: "7 天", value: "7d"},
              {label: "30 天", value: "30d"}
            ]}
            onChange={(value) => setPeriod(value as typeof period)}
          />
        }
        className="dashboard-card"
      >
        <Row gutter={[16, 16]}>
          <Col xs={24} lg={8}>
            <MiniTrend title="登录次数" points={points} field="login_count" color="#3568f1" />
          </Col>
          <Col xs={24} lg={8}>
            <MiniTrend title="创建房间" points={points} field="rooms_created" color="#1d9b73" />
          </Col>
          <Col xs={24} lg={8}>
            <MiniTrend title="风险事件" points={points} field="risk_events" color="#e57a21" />
          </Col>
        </Row>
      </Card>

      {summary && alerts.length > 0 && (
        <Card title="当前异常" className="dashboard-card">
          <Row gutter={[12, 12]}>
            {alerts.map((item) => (
              <Col xs={24} lg={8} key={item.id}>
                <Link to={item.resource_path} className="alert-card">
                  <Tag color={item.severity === "CRITICAL" ? "red" : "orange"}>
                    {item.severity}
                  </Tag>
                  <strong>{item.title}</strong>
                  <span>{item.summary}</span>
                  <small>{item.count} 项 · 查看对应资源</small>
                </Link>
              </Col>
            ))}
          </Row>
        </Card>
      )}

      <Row gutter={[16, 16]}>
        <Col xs={24} xl={16}>
          <Card
            title="管理模块"
            extra={
              summary && (
                <Typography.Text type="secondary">
                  更新于 {new Date(summary.generated_at).toLocaleTimeString("zh-CN")}
                </Typography.Text>
              )
            }
            className="dashboard-card"
          >
            <Row gutter={[14, 14]}>
              {modules.map((module) => (
                <Col xs={24} md={12} key={module.title}>
                  <Link
                    to={module.href}
                    className={`module-card ${module.href === "#" ? "disabled" : ""}`}
                    onClick={(event) => {
                      if (module.href === "#") {
                        event.preventDefault();
                      }
                    }}
                  >
                    <div className="module-icon">{module.icon}</div>
                    <div className="module-copy">
                      <div className="module-title-row">
                        <strong>{module.title}</strong>
                        <Tag
                          variant="filled"
                          color={module.status === "已接入" ? "green" : "default"}
                        >
                          {module.status}
                        </Tag>
                      </div>
                      <span>{module.description}</span>
                    </div>
                  </Link>
                </Col>
              ))}
            </Row>
          </Card>
        </Col>
        <Col xs={24} xl={8}>
          <Card title="入口安全链" className="dashboard-card security-card">
            <div className="security-score">
              <Progress
                type="dashboard"
                percent={100}
                size={108}
                strokeColor="#1d9b73"
                format={() => <SafetyCertificateOutlined />}
              />
              <div>
                <Typography.Title level={4}>四层校验生效</Typography.Title>
                <Typography.Text type="secondary">
                  任意一层失败都会阻止登录或越权操作
                </Typography.Text>
              </div>
            </div>
            <Timeline
              items={[
                {
                  color: "green",
                  icon: <CheckCircleFilled />,
                  content: "可信网段与反向代理边界"
                },
                {
                  color: "green",
                  icon: <CheckCircleFilled />,
                  content: "Turnstile 服务端 Siteverify"
                },
                {
                  color: "green",
                  icon: <CheckCircleFilled />,
                  content: "密码、TOTP 与恢复码"
                },
                {
                  color: "green",
                  icon: <CheckCircleFilled />,
                  content: "RBAC 权限与后端审计"
                }
              ]}
            />
          </Card>
        </Col>
      </Row>

      <Card title="实现边界" className="dashboard-card">
        <Row gutter={[24, 18]}>
          {[
            {
              icon: <LockOutlined />,
              title: "独立认证",
              copy: "管理员账号、会话、密钥和权限表均与玩家系统隔离。"
            },
            {
              icon: <DatabaseOutlined />,
              title: "无数据库直连",
              copy: "所有读写经过 Go Control Plane 的领域规则与事务。"
            },
            {
              icon: <AuditOutlined />,
              title: "后端审计",
              copy: "写操作和登录结果由后端记录，敏感 Token 自动排除。"
            },
            {
              icon: <ClockCircleOutlined />,
              title: "短期凭据",
              copy: "Access Token 仅保存在内存，Refresh Cookie 每次轮换。"
            }
          ].map((item) => (
            <Col xs={24} sm={12} xl={6} key={item.title}>
              <Space align="start" className="boundary-item">
                <span className="boundary-icon">{item.icon}</span>
                <span>
                  <strong>{item.title}</strong>
                  <small>{item.copy}</small>
                </span>
              </Space>
            </Col>
          ))}
        </Row>
      </Card>
    </div>
  );
}

function MiniTrend({
  title,
  points,
  field,
  color
}: {
  title: string;
  points: DashboardPoint[];
  field: "login_count" | "rooms_created" | "risk_events";
  color: string;
}) {
  const values = points.map((point) => point[field]);
  const max = Math.max(1, ...values);
  const total = values.reduce((sum, value) => sum + value, 0);
  return (
    <div className="mini-trend">
      <div>
        <Typography.Text type="secondary">{title}</Typography.Text>
        <strong>{total}</strong>
      </div>
      <div className="trend-bars" role="img" aria-label={`${title}趋势，共 ${total}`}>
        {points.map((point) => (
          <span
            key={point.bucket_start}
            title={`${new Date(point.bucket_start).toLocaleString("zh-CN")} · ${point[field]}`}
            style={{
              height: `${Math.max(5, Math.round((point[field] / max) * 100))}%`,
              backgroundColor: color
            }}
          />
        ))}
      </div>
    </div>
  );
}
