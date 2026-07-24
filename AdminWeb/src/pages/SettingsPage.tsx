import {
  ApiOutlined,
  DashboardOutlined,
  LinkOutlined,
  ReloadOutlined,
  SaveOutlined
} from "@ant-design/icons";
import {
  Alert,
  App,
  Button,
  Card,
  Descriptions,
  Form,
  Input,
  Space,
  Spin,
  Switch,
  Tag,
  Typography
} from "antd";
import {useEffect, useMemo, useState} from "react";
import {ApiError, apiRequest, authClient} from "../api/client";
import {OperationReasonModal} from "../components/OperationReasonModal";
import type {AdminCapabilities, AdminSetting} from "../types";

type SettingsForm = Record<string, boolean | string>;

export function SettingsPage() {
  const {message} = App.useApp();
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
        apiRequest<{items: AdminSetting[]}>("/v1/admin/settings"),
        apiRequest<AdminCapabilities>("/v1/admin/capabilities")
      ]);
      setSettings(settingResult.items);
      setCapabilities(capabilityResult);
      form.setFieldsValue(Object.fromEntries(
        settingResult.items.map((setting) => [setting.key, setting.value])
      ));
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const featureSettings = useMemo(
    () => settings.filter((setting) => setting.category === "FEATURES"),
    [settings]
  );
  const integrationSettings = useMemo(
    () => settings.filter((setting) => setting.category === "INTEGRATIONS"),
    [settings]
  );
  const grafanaURL = String(
    settings.find((setting) => setting.key === "integrations.grafana_url")?.value ?? ""
  );
  const runbookURL = String(
    settings.find((setting) => setting.key === "integrations.runbook_base_url")?.value ?? ""
  );

  const prepareSave = async () => {
    const values = await form.validateFields();
    const changed = Object.fromEntries(
      settings
        .filter((setting) => setting.editable && values[setting.key] !== setting.value)
        .map((setting) => [setting.key, values[setting.key]])
    );
    if (Object.keys(changed).length === 0) {
      message.info("没有需要保存的设置变更。");
      return;
    }
    setPendingValues(changed);
    setConfirmOpen(true);
  };

  const save = async ({reason}: {reason: string}) => {
    setWorking(true);
    try {
      await apiRequest<{items: AdminSetting[]}>("/v1/admin/settings", {
        method: "PATCH",
        body: JSON.stringify({values: pendingValues, reason})
      });
      setConfirmOpen(false);
      setPendingValues({});
      await load();
      message.success("系统设置已更新并写入审计日志。");
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  return (
    <div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SYSTEM / GOVERNANCE</Typography.Text>
          <Typography.Title level={2}>系统设置与集成</Typography.Title>
          <Typography.Paragraph type="secondary">
            这里只提供经过后端白名单验证的非秘密配置，不展示数据库、Redis、Token、密钥或私钥。
          </Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={load} loading={loading}>刷新</Button>
          {canUpdate && (
            <Button type="primary" icon={<SaveOutlined />} onClick={prepareSave}>
              保存变更
            </Button>
          )}
        </Space>
      </section>

      {loading ? (
        <Card><Spin /></Card>
      ) : (
        <Form form={form} layout="vertical" requiredMark={false}>
          <div className="settings-grid">
            <Card title="功能开关" className="metric-card">
              <Alert
                showIcon
                type="info"
                message="开关用于能力发现和未来模块显隐；未实现的领域服务不会因前端开关而被绕过。"
                className="action-alert"
              />
              <div className="setting-list">
                {featureSettings.map((setting) => (
                  <div className="setting-row" key={setting.key}>
                    <div>
                      <strong>{setting.display_name}</strong>
                      <span>{setting.description}</span>
                      <code>{setting.key}</code>
                    </div>
                    <Form.Item name={setting.key} valuePropName="checked" noStyle>
                      <Switch
                        disabled={!canUpdate || !setting.editable}
                        checkedChildren="启用"
                        unCheckedChildren="停用"
                      />
                    </Form.Item>
                  </div>
                ))}
              </div>
            </Card>

            <Card title="只读运维集成" className="metric-card">
              {integrationSettings.map((setting) => (
                <Form.Item
                  key={setting.key}
                  label={setting.display_name}
                  name={setting.key}
                  extra={setting.description}
                  rules={[{
                    type: "url",
                    warningOnly: false,
                    message: "请输入完整的 HTTPS URL。"
                  }, {
                    validator: (_, value) =>
                      !value || String(value).startsWith("https://")
                        ? Promise.resolve()
                        : Promise.reject(new Error("仅允许 HTTPS URL。"))
                  }]}
                >
                  <Input
                    prefix={<LinkOutlined />}
                    disabled={!canUpdate || !setting.editable}
                    placeholder="https://..."
                    maxLength={2048}
                  />
                </Form.Item>
              ))}
              <Space wrap>
                {grafanaURL ? (
                  <Button
                    icon={<DashboardOutlined />}
                    href={grafanaURL}
                    target="_blank"
                    rel="noreferrer"
                  >
                    打开 Grafana
                  </Button>
                ) : <Tag>尚未配置 Grafana</Tag>}
                {runbookURL ? (
                  <Button icon={<LinkOutlined />} href={runbookURL} target="_blank" rel="noreferrer">
                    打开 Runbook
                  </Button>
                ) : <Tag>尚未配置 Runbook</Tag>}
              </Space>
            </Card>
          </div>
        </Form>
      )}

      {capabilities && (
        <Card title={<Space><ApiOutlined />API 能力发现</Space>}>
          <Descriptions bordered size="small" column={{xs: 1, md: 2, xl: 4}}>
            <Descriptions.Item label="API 版本">{capabilities.api_version}</Descriptions.Item>
            <Descriptions.Item label="资源数">{capabilities.resources.length}</Descriptions.Item>
            <Descriptions.Item label="最大批量数">{capabilities.max_batch_operations}</Descriptions.Item>
            <Descriptions.Item label="实时订阅">
              {capabilities.realtime_subscriptions ? "支持" : "轮询回退"}
            </Descriptions.Item>
            <Descriptions.Item label="Dashboard 轮询">
              {capabilities.polling_fallback_seconds.dashboard} 秒
            </Descriptions.Item>
            <Descriptions.Item label="Fleet 轮询">
              {capabilities.polling_fallback_seconds.fleet} 秒
            </Descriptions.Item>
            <Descriptions.Item label="普通列表轮询">
              {capabilities.polling_fallback_seconds.lists} 秒
            </Descriptions.Item>
            <Descriptions.Item label="双人审批">
              {capabilities.dual_approval ? "已启用" : "模型已预留"}
            </Descriptions.Item>
          </Descriptions>
        </Card>
      )}

      <OperationReasonModal
        open={confirmOpen}
        title="保存系统设置"
        consequence={`将更新 ${Object.keys(pendingValues).length} 项非秘密配置，并立即写入审计日志。`}
        confirmLabel="确认保存"
        requireMFA
        loading={working}
        onCancel={() => setConfirmOpen(false)}
        onConfirm={save}
      />
    </div>
  );
}

function errorMessage(error: unknown) {
  return error instanceof ApiError ? error.message : "系统设置请求失败。";
}
