import { tr } from "../i18n";
import { ApiOutlined, DashboardOutlined, LinkOutlined, ReloadOutlined, SaveOutlined } from "@ant-design/icons";
import { Alert, App, Button, Card, Descriptions, Form, Input, Space, Spin, Switch, Tag, Typography } from "antd";
import { useEffect, useMemo, useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import { OperationReasonModal } from "../components/OperationReasonModal";
import type { AdminCapabilities, AdminSetting } from "../types";
type SettingsForm = Record<string, boolean | string>;
export function SettingsPage() {
    const { message } = App.useApp();
    const [form] = Form.useForm<SettingsForm>();
    const [settings, setSettings] = useState<AdminSetting[]>([]);
    const [capabilities, setCapabilities] = useState<AdminCapabilities | null>(null);
    const [confirmOpen, setConfirmOpen] = useState(false);
    const [pendingValues, setPendingValues] = useState<SettingsForm>({});
    const [loading, setLoading] = useState(true);
    const [working, setWorking] = useState(false);
    const canUpdate = authClient.permissions().includes("settings.update");
    const load = async () => {
        setLoading(true);
        try {
            const [settingResult, capabilityResult] = await Promise.all([
                apiRequest<{
                    items: AdminSetting[];
                }>("/v1/admin/settings"),
                apiRequest<AdminCapabilities>("/v1/admin/capabilities")
            ]);
            setSettings(settingResult.items);
            setCapabilities(capabilityResult);
            form.setFieldsValue(Object.fromEntries(settingResult.items.map((setting) => [setting.key, setting.value])));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setLoading(false);
        }
    };
    useEffect(() => {
        void load();
    }, []);
    const featureSettings = useMemo(() => settings.filter((setting) => setting.category === "FEATURES"), [settings]);
    const integrationSettings = useMemo(() => settings.filter((setting) => setting.category === "INTEGRATIONS"), [settings]);
    const grafanaURL = String(settings.find((setting) => setting.key === "integrations.grafana_url")?.value ?? "");
    const runbookURL = String(settings.find((setting) => setting.key === "integrations.runbook_base_url")?.value ?? "");
    const prepareSave = async () => {
        const values = await form.validateFields();
        const changed = Object.fromEntries(settings
            .filter((setting) => setting.editable && values[setting.key] !== setting.value)
            .map((setting) => [setting.key, values[setting.key]]));
        if (Object.keys(changed).length === 0) {
            message.info(tr("\u6CA1\u6709\u9700\u8981\u4FDD\u5B58\u7684\u8BBE\u7F6E\u53D8\u66F4\u3002"));
            return;
        }
        setPendingValues(changed);
        setConfirmOpen(true);
    };
    const save = async ({ reason }: {
        reason: string;
    }) => {
        setWorking(true);
        try {
            await apiRequest<{
                items: AdminSetting[];
            }>("/v1/admin/settings", {
                method: "PATCH",
                body: JSON.stringify({ values: pendingValues, reason })
            });
            setConfirmOpen(false);
            setPendingValues({});
            await load();
            message.success(tr("\u7CFB\u7EDF\u8BBE\u7F6E\u5DF2\u66F4\u65B0\u5E76\u5199\u5165\u5BA1\u8BA1\u65E5\u5FD7\u3002"));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SYSTEM / GOVERNANCE</Typography.Text>
          <Typography.Title level={2}>{tr("\u7CFB\u7EDF\u8BBE\u7F6E\u4E0E\u96C6\u6210")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("\u8FD9\u91CC\u53EA\u63D0\u4F9B\u7ECF\u8FC7\u540E\u7AEF\u767D\u540D\u5355\u9A8C\u8BC1\u7684\u975E\u79D8\u5BC6\u914D\u7F6E\uFF0C\u4E0D\u5C55\u793A\u6570\u636E\u5E93\u3001Redis\u3001Token\u3001\u5BC6\u94A5\u6216\u79C1\u94A5\u3002")}</Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={load} loading={loading}>{tr("\u5237\u65B0")}</Button>
          {canUpdate && (<Button type="primary" icon={<SaveOutlined />} onClick={prepareSave}>{tr("\u4FDD\u5B58\u53D8\u66F4")}</Button>)}
        </Space>
      </section>

      {loading ? (<Card><Spin /></Card>) : (<Form form={form} layout="vertical" requiredMark={false}>
          <div className="settings-grid">
            <Card title={tr("\u529F\u80FD\u5F00\u5173")} className="metric-card">
              <Alert showIcon type="info" message={tr("\u5F00\u5173\u7528\u4E8E\u80FD\u529B\u53D1\u73B0\u548C\u672A\u6765\u6A21\u5757\u663E\u9690\uFF1B\u672A\u5B9E\u73B0\u7684\u9886\u57DF\u670D\u52A1\u4E0D\u4F1A\u56E0\u524D\u7AEF\u5F00\u5173\u800C\u88AB\u7ED5\u8FC7\u3002")} className="action-alert"/>
              <div className="setting-list">
                {featureSettings.map((setting) => (<div className="setting-row" key={setting.key}>
                    <div>
                      <strong>{setting.display_name}</strong>
                      <span>{setting.description}</span>
                      <code>{setting.key}</code>
                    </div>
                    <Form.Item name={setting.key} valuePropName="checked" noStyle>
                      <Switch disabled={!canUpdate || !setting.editable} checkedChildren={tr("\u542F\u7528")} unCheckedChildren={tr("\u505C\u7528")}/>
                    </Form.Item>
                  </div>))}
              </div>
            </Card>

            <Card title={tr("\u53EA\u8BFB\u8FD0\u7EF4\u96C6\u6210")} className="metric-card">
              {integrationSettings.map((setting) => (<Form.Item key={setting.key} label={setting.display_name} name={setting.key} extra={setting.description} rules={[{
                        type: "url",
                        warningOnly: false,
                        message: tr("\u8BF7\u8F93\u5165\u5B8C\u6574\u7684 HTTPS URL\u3002")
                    }, {
                        validator: (_, value) => !value || String(value).startsWith("https://")
                            ? Promise.resolve()
                            : Promise.reject(new Error(tr("\u4EC5\u5141\u8BB8 HTTPS URL\u3002")))
                    }]}>
                  <Input prefix={<LinkOutlined />} disabled={!canUpdate || !setting.editable} placeholder="https://..." maxLength={2048}/>
                </Form.Item>))}
              <Space wrap>
                {grafanaURL ? (<Button icon={<DashboardOutlined />} href={grafanaURL} target="_blank" rel="noreferrer">{tr("\u6253\u5F00 Grafana")}</Button>) : <Tag>{tr("\u5C1A\u672A\u914D\u7F6E Grafana")}</Tag>}
                {runbookURL ? (<Button icon={<LinkOutlined />} href={runbookURL} target="_blank" rel="noreferrer">{tr("\u6253\u5F00 Runbook")}</Button>) : <Tag>{tr("\u5C1A\u672A\u914D\u7F6E Runbook")}</Tag>}
              </Space>
            </Card>
          </div>
        </Form>)}

      {capabilities && (<Card title={<Space><ApiOutlined />{tr("API \u80FD\u529B\u53D1\u73B0")}</Space>}>
          <Descriptions bordered size="small" column={{ xs: 1, md: 2, xl: 4 }}>
            <Descriptions.Item label={tr("API \u7248\u672C")}>{capabilities.api_version}</Descriptions.Item>
            <Descriptions.Item label={tr("\u8D44\u6E90\u6570")}>{capabilities.resources.length}</Descriptions.Item>
            <Descriptions.Item label={tr("\u6700\u5927\u6279\u91CF\u6570")}>{capabilities.max_batch_operations}</Descriptions.Item>
            <Descriptions.Item label={tr("\u5B9E\u65F6\u8BA2\u9605")}>
              {capabilities.realtime_subscriptions ? tr("\u652F\u6301") : tr("\u8F6E\u8BE2\u56DE\u9000")}
            </Descriptions.Item>
            <Descriptions.Item label={tr("Dashboard \u8F6E\u8BE2")}>
              {capabilities.polling_fallback_seconds.dashboard}{tr("\u79D2")}</Descriptions.Item>
            <Descriptions.Item label={tr("Fleet \u8F6E\u8BE2")}>
              {capabilities.polling_fallback_seconds.fleet}{tr("\u79D2")}</Descriptions.Item>
            <Descriptions.Item label={tr("\u666E\u901A\u5217\u8868\u8F6E\u8BE2")}>
              {capabilities.polling_fallback_seconds.lists}{tr("\u79D2")}</Descriptions.Item>
            <Descriptions.Item label={tr("\u53CC\u4EBA\u5BA1\u6279")}>
              {capabilities.dual_approval ? tr("\u5DF2\u542F\u7528") : tr("\u6A21\u578B\u5DF2\u9884\u7559")}
            </Descriptions.Item>
          </Descriptions>
        </Card>)}

      <OperationReasonModal open={confirmOpen} title={tr("\u4FDD\u5B58\u7CFB\u7EDF\u8BBE\u7F6E")} consequence={tr(`将更新 ${Object.keys(pendingValues).length} 项非秘密配置，并立即写入审计日志。`)} confirmLabel={tr("\u786E\u8BA4\u4FDD\u5B58")} requireMFA loading={working} onCancel={() => setConfirmOpen(false)} onConfirm={save}/>
    </div>);
}
function errorMessage(error: unknown) {
    return error instanceof ApiError ? error.message : tr("\u7CFB\u7EDF\u8BBE\u7F6E\u8BF7\u6C42\u5931\u8D25\u3002");
}
