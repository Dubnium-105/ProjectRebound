import {
  CopyOutlined,
  EditOutlined,
  PlusOutlined,
  ReloadOutlined,
  SafetyCertificateOutlined
} from "@ant-design/icons";
import {useList} from "@refinedev/core";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Form,
  Input,
  Modal,
  QRCode,
  Select,
  Space,
  Table,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import {useEffect, useState} from "react";
import {ApiError, apiRequest, authClient} from "../api/client";
import {OperationReasonModal} from "../components/OperationReasonModal";
import type {GovernedAdministrator, GovernedRole} from "../types";

type CreateValues = {
  username: string;
  display_name: string;
  password: string;
  roles: string[];
  reason: string;
  mfa_code: string;
};

type EditValues = {
  display_name: string;
  status: "ACTIVE" | "DISABLED";
  roles: string[];
  revoke_sessions: boolean;
  reason: string;
  mfa_code: string;
};

type ProvisioningResult = {
  admin: GovernedAdministrator;
  totp_provisioning_uri: string;
  recovery_codes: string[];
};

export function AdministratorsPage() {
  const {message} = App.useApp();
  const permissions = authClient.permissions();
  const [createForm] = Form.useForm<CreateValues>();
  const [editForm] = Form.useForm<EditValues>();
  const [roles, setRoles] = useState<GovernedRole[]>([]);
  const [createOpen, setCreateOpen] = useState(false);
  const [editing, setEditing] = useState<GovernedAdministrator | null>(null);
  const [resetting, setResetting] = useState<GovernedAdministrator | null>(null);
  const [provisioning, setProvisioning] = useState<ProvisioningResult | null>(null);
  const [working, setWorking] = useState(false);
  const {query, result} = useList<GovernedAdministrator>({
    resource: "administrators",
    pagination: {pageSize: 100}
  });

  useEffect(() => {
    apiRequest<{items: GovernedRole[]; permissions: unknown[]}>("/v1/admin/roles")
      .then((result) => setRoles(result.items))
      .catch((error) => message.error(errorMessage(error)));
  }, [message]);

  const roleOptions = roles.map((role) => ({
    label: `${role.display_name} (${role.name})`,
    value: role.name
  }));

  const openCreate = () => {
    createForm.resetFields();
    createForm.setFieldsValue({roles: [], reason: ""});
    setCreateOpen(true);
  };

  const createAdministrator = async (values: CreateValues) => {
    setWorking(true);
    try {
      await authClient.stepUp(values.mfa_code);
      const result = await apiRequest<ProvisioningResult>("/v1/admin/admins", {
        method: "POST",
        body: JSON.stringify({
          username: values.username,
          display_name: values.display_name,
          password: values.password,
          roles: values.roles,
          reason: values.reason
        })
      });
      createForm.resetFields();
      setCreateOpen(false);
      setProvisioning(result);
      await query.refetch();
      message.success("管理员已创建。请立即交付一次性 MFA 配置。");
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const openEdit = (administrator: GovernedAdministrator) => {
    editForm.setFieldsValue({
      display_name: administrator.display_name,
      status: administrator.status,
      roles: administrator.roles,
      revoke_sessions: false,
      reason: "",
      mfa_code: ""
    });
    setEditing(administrator);
  };

  const updateAdministrator = async (values: EditValues) => {
    if (!editing) return;
    setWorking(true);
    try {
      await authClient.stepUp(values.mfa_code);
      await apiRequest<GovernedAdministrator>(
        `/v1/admin/admins/${encodeURIComponent(editing.id)}`,
        {
          method: "PATCH",
          body: JSON.stringify({
            display_name: values.display_name,
            status: values.status,
            roles: values.roles,
            revoke_sessions: values.revoke_sessions,
            reason: values.reason
          })
        }
      );
      editForm.resetFields();
      setEditing(null);
      await query.refetch();
      message.success("管理员资料和权限分配已更新。");
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const resetMFA = async ({reason}: {reason: string}) => {
    if (!resetting) return;
    setWorking(true);
    try {
      const result = await apiRequest<ProvisioningResult>(
        `/v1/admin/admins/${encodeURIComponent(resetting.id)}/reset-mfa`,
        {method: "POST", body: JSON.stringify({reason})}
      );
      setResetting(null);
      setProvisioning(result);
      await query.refetch();
      message.success("MFA 已重置，原有管理员会话均已撤销。");
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const copyProvisioning = async () => {
    if (!provisioning) return;
    const content = [
      `TOTP: ${provisioning.totp_provisioning_uri}`,
      "",
      "恢复码（每个仅可使用一次）：",
      ...provisioning.recovery_codes
    ].join("\n");
    await navigator.clipboard.writeText(content);
    message.success("一次性 MFA 配置已复制到剪贴板。");
  };

  const columns: TableColumnsType<GovernedAdministrator> = [
    {
      title: "管理员",
      dataIndex: "username",
      render: (username: string, item) => (
        <div className="primary-cell">
          <strong>{item.display_name}</strong>
          <span>{username}</span>
        </div>
      )
    },
    {
      title: "状态",
      dataIndex: "status",
      width: 110,
      render: (status: GovernedAdministrator["status"]) => (
        <Tag color={status === "ACTIVE" ? "green" : "default"}>{status}</Tag>
      )
    },
    {
      title: "角色",
      dataIndex: "roles",
      render: (assignedRoles: string[]) => (
        <Space size={[4, 4]} wrap>
          {assignedRoles.map((role) => (
            <Tag color={role === "SUPER_ADMIN" ? "purple" : "blue"} key={role}>{role}</Tag>
          ))}
        </Space>
      )
    },
    {
      title: "MFA",
      dataIndex: "mfa_enabled",
      width: 90,
      render: (enabled: boolean) => (
        <Tag color={enabled ? "green" : "red"}>{enabled ? "已启用" : "异常"}</Tag>
      )
    },
    {
      title: "最后登录",
      dataIndex: "last_login_at",
      width: 180,
      render: (value?: string) => value ? formatTime(value) : "尚未登录"
    },
    {
      title: "操作",
      key: "actions",
      width: 230,
      fixed: "right",
      render: (_, item) => permissions.includes("admins.update") ? (
        <Space size={2}>
          <Button type="link" icon={<EditOutlined />} onClick={() => openEdit(item)}>
            编辑
          </Button>
          <Button
            type="link"
            icon={<SafetyCertificateOutlined />}
            onClick={() => setResetting(item)}
          >
            重置 MFA
          </Button>
        </Space>
      ) : null
    }
  ];

  return (
    <div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">SECURITY / ADMINISTRATORS</Typography.Text>
          <Typography.Title level={2}>管理员账号</Typography.Title>
          <Typography.Paragraph type="secondary">
            管理员身份与玩家账号隔离；高风险变更要求权限、操作原因和短时 MFA 提权。
          </Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>
            刷新
          </Button>
          {permissions.includes("admins.create") && (
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
              新建管理员
            </Button>
          )}
        </Space>
      </section>

      <Card className="table-card">
        <Table<GovernedAdministrator>
          rowKey="id"
          columns={columns}
          dataSource={result.data}
          loading={query.isLoading}
          pagination={false}
          scroll={{x: 1050}}
          locale={{emptyText: "暂无管理员账号。"}}
        />
      </Card>

      <Modal
        open={createOpen}
        title="新建管理员"
        width={680}
        okText="创建并生成 MFA"
        confirmLoading={working}
        onCancel={() => !working && setCreateOpen(false)}
        onOk={() => createForm.submit()}
        destroyOnHidden
      >
        <Alert
          showIcon
          type="info"
          message="密码只用于本次创建；TOTP URI 和恢复码将在成功后显示一次。"
          className="action-alert"
        />
        <Form form={createForm} layout="vertical" requiredMark={false} onFinish={createAdministrator}>
          <Space align="start" style={{width: "100%"}} wrap>
            <Form.Item
              label="登录名"
              name="username"
              rules={[{required: true, whitespace: true}, {max: 128}]}
            >
              <Input autoComplete="off" maxLength={128} />
            </Form.Item>
            <Form.Item
              label="显示名"
              name="display_name"
              rules={[{required: true, whitespace: true}, {max: 128}]}
            >
              <Input maxLength={128} />
            </Form.Item>
          </Space>
          <Form.Item
            label="初始密码"
            name="password"
            rules={[
              {required: true, message: "请输入至少 12 个字符的初始密码。"},
              {min: 12, max: 256}
            ]}
          >
            <Input.Password autoComplete="new-password" />
          </Form.Item>
          <Form.Item label="角色" name="roles" rules={[{required: true, type: "array", min: 1}]}>
            <Select mode="multiple" options={roleOptions} optionFilterProp="label" />
          </Form.Item>
          <Form.Item
            label="操作原因"
            name="reason"
            rules={[{required: true, whitespace: true}, {max: 500}]}
          >
            <Input.TextArea rows={3} maxLength={500} showCount />
          </Form.Item>
          <Form.Item
            label="当前管理员 TOTP 或恢复码"
            name="mfa_code"
            rules={[{required: true, whitespace: true}, {min: 6, max: 32}]}
          >
            <Input.Password autoComplete="one-time-code" maxLength={32} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        open={Boolean(editing)}
        title={`编辑管理员 · ${editing?.username ?? ""}`}
        width={680}
        okText="确认更新"
        confirmLoading={working}
        okButtonProps={{danger: editing?.status === "ACTIVE"}}
        onCancel={() => !working && setEditing(null)}
        onOk={() => editForm.submit()}
        destroyOnHidden
      >
        <Form form={editForm} layout="vertical" requiredMark={false} onFinish={updateAdministrator}>
          <Form.Item label="显示名" name="display_name" rules={[{required: true, whitespace: true}, {max: 128}]}>
            <Input maxLength={128} />
          </Form.Item>
          <Form.Item label="状态" name="status" rules={[{required: true}]}>
            <Select options={[
              {label: "ACTIVE · 可登录", value: "ACTIVE"},
              {label: "DISABLED · 禁止登录并撤销会话", value: "DISABLED"}
            ]} />
          </Form.Item>
          <Form.Item label="角色" name="roles" rules={[{required: true, type: "array", min: 1}]}>
            <Select mode="multiple" options={roleOptions} optionFilterProp="label" />
          </Form.Item>
          <Form.Item name="revoke_sessions" valuePropName="checked">
            <Checkbox>立即撤销该管理员的全部现有会话</Checkbox>
          </Form.Item>
          <Form.Item label="操作原因" name="reason" rules={[{required: true, whitespace: true}, {max: 500}]}>
            <Input.TextArea rows={3} maxLength={500} showCount />
          </Form.Item>
          <Form.Item
            label="当前管理员 TOTP 或恢复码"
            name="mfa_code"
            rules={[{required: true, whitespace: true}, {min: 6, max: 32}]}
          >
            <Input.Password autoComplete="one-time-code" maxLength={32} />
          </Form.Item>
        </Form>
      </Modal>

      <OperationReasonModal
        open={Boolean(resetting)}
        title={`重置 MFA · ${resetting?.username ?? ""}`}
        consequence="原 TOTP、恢复码和全部登录会话将立即失效；新配置只显示一次。"
        confirmLabel="重置并撤销会话"
        danger
        requireMFA
        loading={working}
        onCancel={() => setResetting(null)}
        onConfirm={resetMFA}
      />

      <Modal
        open={Boolean(provisioning)}
        title="一次性 MFA 配置"
        width={720}
        okText="我已安全保存"
        cancelButtonProps={{style: {display: "none"}}}
        onOk={() => setProvisioning(null)}
        onCancel={() => setProvisioning(null)}
        destroyOnHidden
      >
        {provisioning && (
          <div className="page-stack">
            <Alert
              showIcon
              type="warning"
              message={`请立即交付给 ${provisioning.admin.display_name}；关闭后无法再次查看。`}
            />
            <div className="provisioning-grid">
              <QRCode value={provisioning.totp_provisioning_uri} size={190} bordered={false} />
              <div>
                <Typography.Text strong>TOTP Provisioning URI</Typography.Text>
                <Typography.Paragraph copyable code className="secret-uri">
                  {provisioning.totp_provisioning_uri}
                </Typography.Paragraph>
              </div>
            </div>
            <div>
              <Typography.Text strong>一次性恢复码</Typography.Text>
              <div className="recovery-code-grid">
                {provisioning.recovery_codes.map((code) => <code key={code}>{code}</code>)}
              </div>
            </div>
            <Button icon={<CopyOutlined />} onClick={copyProvisioning}>
              复制完整配置
            </Button>
          </div>
        )}
      </Modal>
    </div>
  );
}

function formatTime(value: string) {
  return new Date(value).toLocaleString("zh-CN", {hour12: false});
}

function errorMessage(error: unknown) {
  return error instanceof ApiError ? error.message : "管理员治理请求失败。";
}
