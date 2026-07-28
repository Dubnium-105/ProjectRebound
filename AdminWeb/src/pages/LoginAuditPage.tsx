import { localeTag, tr } from "../i18n";
import { ReloadOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { Button, Card, Descriptions, Table, Tag, Typography, type TableColumnsType } from "antd";
import type { LoginAudit } from "../types";
function loginAuditColumns(): TableColumnsType<LoginAudit> {
    return [
    {
        title: tr("\u767B\u5F55\u4E8B\u4EF6"),
        dataIndex: "event_type",
        render: (value: string, item) => (<div className="primary-cell">
        <strong>{value}</strong>
        <span>{item.id}</span>
      </div>)
    },
    {
        title: tr("\u7ED3\u679C"),
        dataIndex: "result",
        width: 110,
        render: (value: LoginAudit["result"]) => (<Tag color={value === "SUCCESS" ? "green" : "red"}>{value}</Tag>)
    },
    {
        title: tr("\u5931\u8D25\u539F\u56E0"),
        dataIndex: "reason_code",
        width: 190,
        render: (value: string) => value || "—"
    },
    {
        title: "Turnstile",
        dataIndex: "turnstile_success",
        width: 130,
        render: (value: boolean | null) => value === null ? (<Tag>{tr("\u672A\u6267\u884C")}</Tag>) : (<Tag color={value ? "green" : "red"}>{value ? tr("\u901A\u8FC7") : tr("\u5931\u8D25")}</Tag>)
    },
    {
        title: tr("\u6765\u6E90\u5730\u5740"),
        dataIndex: "ip_address",
        width: 150,
        render: (value: string) => value || "—"
    },
    {
        title: tr("\u65F6\u95F4"),
        dataIndex: "created_at",
        width: 180,
        render: formatTime
    }
    ];
}
export function LoginAuditPage() {
    const columns = loginAuditColumns();
    const { query, result } = useList<LoginAudit>({
        resource: "login-audit",
        pagination: { pageSize: 100 }
    });
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SECURITY / LOGIN AUDIT</Typography.Text>
          <Typography.Title level={2}>{tr("\u7BA1\u7406\u5458\u767B\u5F55\u5BA1\u8BA1")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u4EC5\u663E\u793A Turnstile \u6821\u9A8C\u7ED3\u8BBA\u548C\u8BCA\u65AD\u5B57\u6BB5\uFF1B\u4E0D\u4F1A\u4FDD\u5B58 Widget Token\u3001Secret\u3001\u5BC6\u7801\u6216 Cookie\u3002")}</Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
      </section>
      <Card className="table-card">
        <Table<LoginAudit> rowKey="id" columns={columns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1100 }} expandable={{
            expandedRowRender: (item) => (<Descriptions bordered size="small" column={{ xs: 1, lg: 2 }}>
                <Descriptions.Item label={tr("\u7BA1\u7406\u5458")}>{item.admin_id || tr("\u672A\u77E5\u8D26\u53F7")}</Descriptions.Item>
                <Descriptions.Item label={tr("\u8BF7\u6C42\u7F16\u53F7")}>
                  <Typography.Text copyable>{item.request_id || "—"}</Typography.Text>
                </Descriptions.Item>
                <Descriptions.Item label="Turnstile hostname">
                  {item.turnstile_hostname || "—"}
                </Descriptions.Item>
                <Descriptions.Item label="Turnstile action">
                  {item.turnstile_action || "—"}
                </Descriptions.Item>
                <Descriptions.Item label={tr("Siteverify \u5EF6\u8FDF")}>
                  {item.turnstile_verify_latency_ms === null
                    ? "—"
                    : `${item.turnstile_verify_latency_ms} ms`}
                </Descriptions.Item>
                <Descriptions.Item label={tr("\u9519\u8BEF\u7801")}>
                  {item.turnstile_error_codes.length > 0
                    ? item.turnstile_error_codes.join(", ")
                    : "—"}
                </Descriptions.Item>
                <Descriptions.Item label="User-Agent" span={2}>
                  {item.user_agent || "—"}
                </Descriptions.Item>
              </Descriptions>)
        }} locale={{ emptyText: tr("\u5F53\u524D\u6CA1\u6709\u7BA1\u7406\u5458\u767B\u5F55\u5BA1\u8BA1\u8BB0\u5F55\u3002") }}/>
      </Card>
    </div>);
}
function formatTime(value: string) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString(localeTag());
}
