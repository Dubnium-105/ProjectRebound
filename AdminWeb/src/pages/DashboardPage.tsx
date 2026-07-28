import { localeTag, localizeSystemText, tr } from "../i18n";
import { ApiOutlined, AuditOutlined, CheckCircleFilled, ClockCircleOutlined, DatabaseOutlined, KeyOutlined, LockOutlined, SafetyCertificateOutlined, TeamOutlined } from "@ant-design/icons";
import { Alert, Card, Col, Progress, Row, Segmented, Skeleton, Space, Statistic, Tag, Timeline, Typography } from "antd";
import { useEffect, useState } from "react";
import { Link } from "react-router";
import { ApiError, apiRequest } from "../api/client";
import type { DashboardAlert, DashboardPoint, DashboardSummary } from "../types";
function dashboardModules() {
    return [
    {
        icon: <TeamOutlined />,
        title: tr("\u73A9\u5BB6\u4E0E\u4F1A\u8BDD"),
        description: tr("\u67E5\u8BE2\u73A9\u5BB6\u3001\u53D8\u66F4\u8D26\u53F7\u72B6\u6001\u3001\u64A4\u9500\u767B\u5F55\u4F1A\u8BDD"),
        href: "/players",
        status: tr("\u5DF2\u63A5\u5165")
    },
    {
        icon: <KeyOutlined />,
        title: tr("\u9080\u8BF7\u7801"),
        description: tr("\u67E5\u770B\u9080\u8BF7\u7801\u6279\u6B21\u3001\u4F7F\u7528\u60C5\u51B5\u4E0E\u6709\u6548\u671F"),
        href: "/invite-codes",
        status: tr("\u5DF2\u63A5\u5165")
    },
    {
        icon: <SafetyCertificateOutlined />,
        title: tr("\u767B\u5F55\u98CE\u9669"),
        description: tr("\u67E5\u770B\u5F02\u5E38\u8BA4\u8BC1\u4E8B\u4EF6\u53CA\u5173\u8054\u8BF7\u6C42"),
        href: "/risk-events",
        status: tr("\u5DF2\u63A5\u5165")
    },
    {
        icon: <ApiOutlined />,
        title: tr("\u8054\u673A\u8D44\u6E90"),
        description: tr("\u623F\u95F4\u3001\u4E13\u670D\u3001\u4E2D\u7EE7\u8282\u70B9\u4E0E\u6D3B\u52A8\u8FDE\u63A5"),
        href: "/online/connections",
        status: tr("\u5DF2\u63A5\u5165")
    }
    ];
}
export function DashboardPage() {
    const modules = dashboardModules();
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
                apiRequest<{
                    items: DashboardAlert[];
                }>("/v1/admin/dashboard/alerts"),
                apiRequest<{
                    items: DashboardPoint[];
                }>(`/v1/admin/dashboard/timeseries?period=${encodeURIComponent(period)}`)
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
                    setLoadError(error instanceof ApiError ? error.message : tr("\u65E0\u6CD5\u8BFB\u53D6\u8FD0\u8425\u6307\u6807\u3002"));
                }
            });
        };
        load();
        const interval = window.setInterval(load, 30000);
        return () => {
            active = false;
            window.clearInterval(interval);
        };
    }, [period]);
    return (<div className="page-stack">
      <section className="page-hero compact">
        <div>
          <Typography.Text className="eyebrow">CONTROL PLANE / OVERVIEW</Typography.Text>
          <Typography.Title level={1}>{tr("\u8FD0\u8425\u603B\u89C8")}</Typography.Title>
          <Typography.Paragraph>{tr("\u7BA1\u7406\u5165\u53E3\u5DF2\u7ECF\u4E0E\u73A9\u5BB6\u8BA4\u8BC1\u5206\u79BB\u3002\u5F53\u524D\u63A7\u5236\u53F0\u53EA\u5C55\u793A\u63A7\u5236\u9762\u660E\u786E\u6388\u6743\u7684\u6570\u636E\u4E0E\u64CD\u4F5C\uFF0C\u4E0D\u76F4\u63A5\u8FDE\u63A5\u6570\u636E\u5E93\u6216\u4E2D\u7EE7\u8282\u70B9\u3002")}</Typography.Paragraph>
        </div>
        <div className="hero-status">
          <div className="pulse-dot"/>
          <div>
            <strong>{tr("\u5B89\u5168\u4F1A\u8BDD\u6709\u6548")}</strong>
            <span>{tr("\u77ED\u671F Access Token \u00B7 HttpOnly Refresh Cookie")}</span>
          </div>
        </div>
      </section>

      {loadError && (<Alert type="error" showIcon title={tr("\u8FD0\u8425\u6307\u6807\u6682\u65F6\u4E0D\u53EF\u7528")} description={tr(`${loadError} 请稍后刷新；若持续失败，请将页面中的请求编号提供给平台值班人员。`)}/>)}
      <Row gutter={[16, 16]}>
        {[
            [tr("\u5F53\u524D\u5728\u7EBF\u73A9\u5BB6"), summary?.online_players],
            [tr("\u6D3B\u52A8 P2P \u623F\u95F4"), summary?.active_p2p_rooms],
            [tr("\u5728\u7EBF Dedicated Server"), summary?.online_game_servers],
            [tr("READY \u4E2D\u7EE7\u8282\u70B9"), summary?.ready_relay_nodes],
            [tr("\u6D3B\u52A8\u4E2D\u7EE7\u5206\u914D"), summary?.active_relay_allocations],
            [tr("\u5F85\u5904\u7406\u98CE\u9669\u4E8B\u4EF6"), summary?.unresolved_risk_events]
        ].map(([title, value]) => (<Col xs={12} md={8} xl={4} key={String(title)}>
            <Card className="metric-card">
              {summary ? (<Statistic title={title} value={value as number}/>) : (<Skeleton active paragraph={false}/>)}
            </Card>
          </Col>))}
      </Row>

      <Card title={tr("\u8FD0\u8425\u8D8B\u52BF")} extra={<Segmented size="small" value={period} options={[
                { label: tr("1 \u5C0F\u65F6"), value: "1h" },
                { label: tr("24 \u5C0F\u65F6"), value: "24h" },
                { label: tr("7 \u5929"), value: "7d" },
                { label: tr("30 \u5929"), value: "30d" }
            ]} onChange={(value) => setPeriod(value as typeof period)}/>} className="dashboard-card">
        <Row gutter={[16, 16]}>
          <Col xs={24} lg={8}>
            <MiniTrend title={tr("\u767B\u5F55\u6B21\u6570")} points={points} field="login_count" color="#3568f1"/>
          </Col>
          <Col xs={24} lg={8}>
            <MiniTrend title={tr("\u521B\u5EFA\u623F\u95F4")} points={points} field="rooms_created" color="#1d9b73"/>
          </Col>
          <Col xs={24} lg={8}>
            <MiniTrend title={tr("\u98CE\u9669\u4E8B\u4EF6")} points={points} field="risk_events" color="#e57a21"/>
          </Col>
        </Row>
      </Card>

      {summary && alerts.length > 0 && (<Card title={tr("\u5F53\u524D\u5F02\u5E38")} className="dashboard-card">
          <Row gutter={[12, 12]}>
            {alerts.map((item) => {
            const copy = dashboardAlertCopy(item);
            return (<Col xs={24} lg={8} key={item.id}>
                <Link to={item.resource_path} className="alert-card">
                  <Tag color={item.severity === "CRITICAL" ? "red" : "orange"}>
                    {item.severity}
                  </Tag>
                  <strong>{copy.title}</strong>
                  <span>{copy.summary}</span>
                  <small>{item.count}{tr("\u9879 \u00B7 \u67E5\u770B\u5BF9\u5E94\u8D44\u6E90")}</small>
                </Link>
              </Col>);
        })}
          </Row>
        </Card>)}

      <Row gutter={[16, 16]}>
        <Col xs={24} xl={16}>
          <Card title={tr("\u7BA1\u7406\u6A21\u5757")} extra={summary && (<Typography.Text type="secondary">{tr("\u66F4\u65B0\u4E8E")}{new Date(summary.generated_at).toLocaleTimeString(localeTag())}
                </Typography.Text>)} className="dashboard-card">
            <Row gutter={[14, 14]}>
              {modules.map((module) => (<Col xs={24} md={12} key={module.title}>
                  <Link to={module.href} className={`module-card ${module.href === "#" ? "disabled" : ""}`} onClick={(event) => {
                if (module.href === "#") {
                    event.preventDefault();
                }
            }}>
                    <div className="module-icon">{module.icon}</div>
                    <div className="module-copy">
                      <div className="module-title-row">
                        <strong>{module.title}</strong>
                        <Tag variant="filled" color={module.status === tr("\u5DF2\u63A5\u5165") ? "green" : "default"}>
                          {module.status}
                        </Tag>
                      </div>
                      <span>{module.description}</span>
                    </div>
                  </Link>
                </Col>))}
            </Row>
          </Card>
        </Col>
        <Col xs={24} xl={8}>
          <Card title={tr("\u5165\u53E3\u5B89\u5168\u94FE")} className="dashboard-card security-card">
            <div className="security-score">
              <Progress type="dashboard" percent={100} size={108} strokeColor="#1d9b73" format={() => <SafetyCertificateOutlined />}/>
              <div>
                <Typography.Title level={4}>{tr("\u56DB\u5C42\u6821\u9A8C\u751F\u6548")}</Typography.Title>
                <Typography.Text type="secondary">{tr("\u4EFB\u610F\u4E00\u5C42\u5931\u8D25\u90FD\u4F1A\u963B\u6B62\u767B\u5F55\u6216\u8D8A\u6743\u64CD\u4F5C")}</Typography.Text>
              </div>
            </div>
            <Timeline items={[
            {
                color: "green",
                icon: <CheckCircleFilled />,
                content: tr("\u53EF\u4FE1\u7F51\u6BB5\u4E0E\u53CD\u5411\u4EE3\u7406\u8FB9\u754C")
            },
            {
                color: "green",
                icon: <CheckCircleFilled />,
                content: tr("Turnstile \u670D\u52A1\u7AEF Siteverify")
            },
            {
                color: "green",
                icon: <CheckCircleFilled />,
                content: tr("\u5BC6\u7801\u3001TOTP \u4E0E\u6062\u590D\u7801")
            },
            {
                color: "green",
                icon: <CheckCircleFilled />,
                content: tr("RBAC \u6743\u9650\u4E0E\u540E\u7AEF\u5BA1\u8BA1")
            }
        ]}/>
          </Card>
        </Col>
      </Row>

      <Card title={tr("\u5B9E\u73B0\u8FB9\u754C")} className="dashboard-card">
        <Row gutter={[24, 18]}>
          {[
            {
                icon: <LockOutlined />,
                title: tr("\u72EC\u7ACB\u8BA4\u8BC1"),
                copy: tr("\u7BA1\u7406\u5458\u8D26\u53F7\u3001\u4F1A\u8BDD\u3001\u5BC6\u94A5\u548C\u6743\u9650\u8868\u5747\u4E0E\u73A9\u5BB6\u7CFB\u7EDF\u9694\u79BB\u3002")
            },
            {
                icon: <DatabaseOutlined />,
                title: tr("\u65E0\u6570\u636E\u5E93\u76F4\u8FDE"),
                copy: tr("\u6240\u6709\u8BFB\u5199\u7ECF\u8FC7 Go Control Plane \u7684\u9886\u57DF\u89C4\u5219\u4E0E\u4E8B\u52A1\u3002")
            },
            {
                icon: <AuditOutlined />,
                title: tr("\u540E\u7AEF\u5BA1\u8BA1"),
                copy: tr("\u5199\u64CD\u4F5C\u548C\u767B\u5F55\u7ED3\u679C\u7531\u540E\u7AEF\u8BB0\u5F55\uFF0C\u654F\u611F Token \u81EA\u52A8\u6392\u9664\u3002")
            },
            {
                icon: <ClockCircleOutlined />,
                title: tr("\u77ED\u671F\u51ED\u636E"),
                copy: tr("Access Token \u4EC5\u4FDD\u5B58\u5728\u5185\u5B58\uFF0CRefresh Cookie \u6BCF\u6B21\u8F6E\u6362\u3002")
            }
        ].map((item) => (<Col xs={24} sm={12} xl={6} key={item.title}>
              <Space align="start" className="boundary-item">
                <span className="boundary-icon">{item.icon}</span>
                <span>
                  <strong>{item.title}</strong>
                  <small>{item.copy}</small>
                </span>
              </Space>
            </Col>))}
        </Row>
      </Card>
    </div>);
}
function dashboardAlertCopy(item: DashboardAlert) {
    const copy: Record<string, {
        title: string;
        summary: string;
    }> = {
        "relay-unhealthy": {
            title: tr("中继节点异常"),
            summary: tr("存在离线或不健康的中继节点。")
        },
        "game-server-unhealthy": {
            title: tr("专服心跳异常"),
            summary: tr("存在离线或不健康的 Dedicated Server。")
        },
        "critical-risk-events": {
            title: tr("高优先级风险事件"),
            summary: tr("存在尚未处理的高危或严重登录风险事件。")
        }
    };
    return copy[item.id] ?? {
        title: localizeSystemText(item.title, "Operational alert"),
        summary: localizeSystemText(item.summary, "An operational condition requires attention.")
    };
}
function MiniTrend({ title, points, field, color }: {
    title: string;
    points: DashboardPoint[];
    field: "login_count" | "rooms_created" | "risk_events";
    color: string;
}) {
    const values = points.map((point) => point[field]);
    const max = Math.max(1, ...values);
    const total = values.reduce((sum, value) => sum + value, 0);
    return (<div className="mini-trend">
      <div>
        <Typography.Text type="secondary">{title}</Typography.Text>
        <strong>{total}</strong>
      </div>
      <div className="trend-bars" role="img" aria-label={tr(`${title}趋势，共 ${total}`)}>
        {points.map((point) => (<span key={point.bucket_start} title={`${new Date(point.bucket_start).toLocaleString(localeTag())} · ${point[field]}`} style={{
                height: `${Math.max(5, Math.round((point[field] / max) * 100))}%`,
                backgroundColor: color
            }}/>))}
      </div>
    </div>);
}
