import {
  EditOutlined,
  LockOutlined,
  ReloadOutlined,
  SafetyOutlined
} from "@ant-design/icons";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Collapse,
  Form,
  Input,
  Modal,
  Space,
  Spin,
  Tag,
  Typography
} from "antd";
import {useEffect, useMemo, useState} from "react";
import {ApiError, apiRequest, authClient} from "../api/client";
import type {AdminPermissionDefinition, GovernedRole} from "../types";

type RoleCatalog = {
  items: GovernedRole[];
  permissions: AdminPermissionDefinition[];
};

type RoleEditValues = {
  permissions: string[];
  reason: string;
  mfa_code: string;
};

export function RolesPage() {
  const {message} = App.useApp();
  const [form] = Form.useForm<RoleEditValues>();
  const [catalog, setCatalog] = useState<RoleCatalog>({items: [], permissions: []});
  const [editing, setEditing] = useState<GovernedRole | null>(null);
  const [loading, setLoading] = useState(true);
  const [working, setWorking] = useState(false);
  const canManage = authClient.permissions().includes("roles.manage");

  const load = async () => {
    setLoading(true);
    try {
      setCatalog(await apiRequest<RoleCatalog>("/v1/admin/roles"));
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    void load();
  }, []);

  const groupedPermissions = useMemo(() => {
    const groups = new Map<string, AdminPermissionDefinition[]>();
    for (const permission of catalog.permissions) {
      const items = groups.get(permission.resource) ?? [];
      items.push(permission);
      groups.set(permission.resource, items);
    }
    return [...groups.entries()].sort(([left], [right]) => left.localeCompare(right));
  }, [catalog.permissions]);

  const openEdit = (role: GovernedRole) => {
    form.setFieldsValue({
      permissions: role.permissions,
      reason: "",
      mfa_code: ""
    });
    setEditing(role);
  };

  const updateRole = async (values: RoleEditValues) => {
    if (!editing) return;
    setWorking(true);
    try {
      await authClient.stepUp(values.mfa_code);
      const updated = await apiRequest<GovernedRole>(
        `/v1/admin/roles/${encodeURIComponent(editing.id)}`,
        {
          method: "PATCH",
          body: JSON.stringify({
            permissions: values.permissions,
            reason: values.reason
          })
        }
      );
      setCatalog((current) => ({
        ...current,
        items: current.items.map((item) => item.id === updated.id ? updated : item)
      }));
      form.resetFields();
      setEditing(null);
      message.success("角色权限集已原子更新。");
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
          <Typography.Text className="eyebrow">SECURITY / RBAC</Typography.Text>
          <Typography.Title level={2}>角色与权限</Typography.Title>
          <Typography.Paragraph type="secondary">
            权限由后端目录统一定义；角色变更会被记录到审计日志，并要求当前管理员再次完成 MFA。
          </Typography.Paragraph>
        </div>
        <Button icon={<ReloadOutlined />} onClick={load} loading={loading}>刷新</Button>
      </section>

      <Alert
        showIcon
        type="info"
        message="SUPER_ADMIN 始终拥有全部权限且不可修改，避免出现权限目录与实际授权漂移。"
      />

      {loading ? (
        <Card><Spin /></Card>
      ) : (
        <div className="role-card-grid">
          {catalog.items.map((role) => (
            <Card
              key={role.id}
              className="metric-card"
              title={
                <Space>
                  {role.name === "SUPER_ADMIN" ? <LockOutlined /> : <SafetyOutlined />}
                  <span>{role.display_name}</span>
                </Space>
              }
              extra={
                canManage && role.name !== "SUPER_ADMIN" ? (
                  <Button type="link" icon={<EditOutlined />} onClick={() => openEdit(role)}>
                    编辑权限
                  </Button>
                ) : null
              }
            >
              <Typography.Paragraph type="secondary">{role.description}</Typography.Paragraph>
              <Space size={[4, 4]} wrap>
                <Tag color={role.system_role ? "purple" : "blue"}>{role.name}</Tag>
                <Tag>{role.permissions.length} 项权限</Tag>
              </Space>
              <Collapse
                ghost
                size="small"
                items={[{
                  key: "permissions",
                  label: "查看权限明细",
                  children: (
                    <Space size={[4, 6]} wrap>
                      {role.permissions.map((permission) => (
                        <Tag key={permission}>{permission}</Tag>
                      ))}
                    </Space>
                  )
                }]}
              />
            </Card>
          ))}
        </div>
      )}

      <Modal
        open={Boolean(editing)}
        title={`编辑角色 · ${editing?.display_name ?? ""}`}
        width={900}
        okText="确认替换权限集"
        confirmLoading={working}
        onCancel={() => !working && setEditing(null)}
        onOk={() => form.submit()}
        destroyOnHidden
      >
        <Alert
          showIcon
          type="warning"
          message="保存后，所有拥有该角色的管理员会在下一次会话校验时使用新权限。"
          className="action-alert"
        />
        <Form form={form} layout="vertical" requiredMark={false} onFinish={updateRole}>
          <Form.Item label="权限" name="permissions">
            <Checkbox.Group className="permission-catalog">
              {groupedPermissions.map(([resource, permissions]) => (
                <Card key={resource} size="small" title={resource}>
                  <div className="permission-list">
                    {permissions.map((permission) => (
                      <Checkbox key={permission.key} value={permission.key}>
                        <span className="permission-copy">
                          <span>
                            <code>{permission.key}</code>
                            <Tag color={riskColor[permission.risk_level]}>{permission.risk_level}</Tag>
                          </span>
                          <small>{permission.description}</small>
                        </span>
                      </Checkbox>
                    ))}
                  </div>
                </Card>
              ))}
            </Checkbox.Group>
          </Form.Item>
          <Form.Item
            label="操作原因"
            name="reason"
            rules={[{required: true, whitespace: true}, {max: 500}]}
          >
            <Input.TextArea rows={3} maxLength={500} showCount />
          </Form.Item>
          <Form.Item
            label="TOTP 或恢复码"
            name="mfa_code"
            extra="MFA 提权凭证只保存在当前浏览器内存中，并绑定当前 Session。"
            rules={[{required: true, whitespace: true}, {min: 6, max: 32}]}
          >
            <Input.Password autoComplete="one-time-code" maxLength={32} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}

const riskColor: Record<AdminPermissionDefinition["risk_level"], string> = {
  LOW: "default",
  MEDIUM: "blue",
  HIGH: "orange",
  CRITICAL: "red"
};

function errorMessage(error: unknown) {
  return error instanceof ApiError ? error.message : "角色权限请求失败。";
}
