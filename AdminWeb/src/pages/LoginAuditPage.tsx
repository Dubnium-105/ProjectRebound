import {ReloadOutlined} from "@ant-design/icons";
import {useList} from "@refinedev/core";
import {
  Button,
  Card,
  Descriptions,
  Table,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import type {LoginAudit} from "../types";

const columns: TableColumnsType<LoginAudit> = [
  {
    title: "登录事件",
    dataIndex: "event_type",
    render: (value: string, item) => (
      <div className="primary-cell">
        <strong>{value}</strong>
        <span>{item.id}</span>
      </div>
    )
  },
  {
    title: "结果",
    dataIndex: "result",
    width: 110,
    render: (value: LoginAudit["result"]) => (
      <Tag color={value === "SUCCESS" ? "green" : "red"}>{value}</Tag>
    )
  },
  {
    title: "失败原因",
    dataIndex: "reason_code",
    width: 190,
    render: (value: string) => value || "—"
  },
  {
    title: "Turnstile",
    dataIndex: "turnstile_success",
    width: 130,
    render: (value: boolean | null) =>
      value === null ? (
        <Tag>未执行</Tag>
      ) : (
        <Tag color={value ? "green" : "red"}>{value ? "通过" : "失败"}</Tag>
      )
  },
  {
    title: "来源地址",
    dataIndex: "ip_address",
    width: 150,
    render: (value: string) => value || "—"
  },
  {
    title: "时间",
    dataIndex: "created_at",
    width: 180,
    render: formatTime
  }
];

export function LoginAuditPage() {
  const {query, result} = useList<LoginAudit>({
    resource: "login-audit",
    pagination: {pageSize: 100}
  });

  return (
    <div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SECURITY / LOGIN AUDIT</Typography.Text>
          <Typography.Title level={2}>管理员登录审计</Typography.Title>
          <Typography.Paragraph type="secondary">
            仅显示 Turnstile 校验结论和诊断字段；不会保存 Widget Token、Secret、密码或 Cookie。
          </Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>
          刷新
        </Button>
      </section>
      <Card className="table-card">
        <Table<LoginAudit>
          rowKey="id"
          columns={columns}
          dataSource={result.data}
          loading={query.isLoading}
          pagination={false}
          scroll={{x: 1100}}
          expandable={{
            expandedRowRender: (item) => (
              <Descriptions bordered size="small" column={{xs: 1, lg: 2}}>
                <Descriptions.Item label="管理员">{item.admin_id || "未知账号"}</Descriptions.Item>
                <Descriptions.Item label="请求编号">
                  <Typography.Text copyable>{item.request_id || "—"}</Typography.Text>
                </Descriptions.Item>
                <Descriptions.Item label="Turnstile hostname">
                  {item.turnstile_hostname || "—"}
                </Descriptions.Item>
                <Descriptions.Item label="Turnstile action">
                  {item.turnstile_action || "—"}
                </Descriptions.Item>
                <Descriptions.Item label="Siteverify 延迟">
                  {item.turnstile_verify_latency_ms === null
                    ? "—"
                    : `${item.turnstile_verify_latency_ms} ms`}
                </Descriptions.Item>
                <Descriptions.Item label="错误码">
                  {item.turnstile_error_codes.length > 0
                    ? item.turnstile_error_codes.join(", ")
                    : "—"}
                </Descriptions.Item>
                <Descriptions.Item label="User-Agent" span={2}>
                  {item.user_agent || "—"}
                </Descriptions.Item>
              </Descriptions>
            )
          }}
          locale={{emptyText: "当前没有管理员登录审计记录。"}}
        />
      </Card>
    </div>
  );
}

function formatTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN");
}
