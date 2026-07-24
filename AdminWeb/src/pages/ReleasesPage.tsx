import {
  CheckCircleOutlined,
  EyeOutlined,
  InboxOutlined,
  MinusCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  RocketOutlined,
  RollbackOutlined
} from "@ant-design/icons";
import {useList} from "@refinedev/core";
import {
  Alert,
  App,
  Button,
  Card,
  Checkbox,
  Descriptions,
  Drawer,
  Form,
  Input,
  InputNumber,
  Modal,
  Result,
  Select,
  Space,
  Steps,
  Table,
  Tag,
  Typography,
  type TableColumnsType
} from "antd";
import {useState} from "react";
import {ApiError, apiRequest, authClient} from "../api/client";
import {OperationReasonModal} from "../components/OperationReasonModal";
import type {Release, ReleaseSourceFile} from "../types";

type CreateValues = {
  platform: string;
  architecture: string;
  channel: "stable" | "beta";
  version: string;
  minimum_supported_version: string;
  force_update: boolean;
  files: ReleaseSourceFile[];
  reason: string;
};

type ReleaseOperation = "validate" | "publish" | "rollback" | "archive";

export function ReleasesPage() {
  const {message} = App.useApp();
  const [form] = Form.useForm<CreateValues>();
  const [createOpen, setCreateOpen] = useState(false);
  const [createStep, setCreateStep] = useState(0);
  const [detail, setDetail] = useState<Release | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [operation, setOperation] = useState<{release: Release; operation: ReleaseOperation} | null>(null);
  const [working, setWorking] = useState(false);
  const permissions = authClient.permissions();
  const {query, result} = useList<Release>({
    resource: "releases",
    pagination: {pageSize: 100}
  });

  const openCreate = () => {
    form.setFieldsValue({
      platform: "windows",
      architecture: "amd64",
      channel: "stable",
      version: "",
      minimum_supported_version: "",
      force_update: false,
      files: [{file_id: "", path: "", size: 0, sha256: "", compression: "none", object_key: ""}],
      reason: ""
    });
    setCreateStep(0);
    setCreateOpen(true);
  };

  const nextStep = async () => {
    const fields = createStep === 0
      ? ["platform", "architecture", "channel", "version", "minimum_supported_version", "force_update"]
      : ["files"];
    await form.validateFields(fields);
    setCreateStep((value) => Math.min(2, value + 1));
  };

  const create = async (values: CreateValues) => {
    setWorking(true);
    try {
      await apiRequest("/v1/admin/releases", {
        method: "POST",
        body: JSON.stringify(values)
      });
      message.success("DRAFT 发布版本已创建，请执行发布前校验。");
      setCreateOpen(false);
      await query.refetch();
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const showDetail = async (release: Release) => {
    setDetail(release);
    setDetailLoading(true);
    try {
      setDetail(await apiRequest<Release>(`/v1/admin/releases/${encodeURIComponent(release.id)}`));
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setDetailLoading(false);
    }
  };

  const execute = async ({reason}: {reason: string}) => {
    if (!operation) return;
    setWorking(true);
    try {
      const updated = await apiRequest<Release>(
        `/v1/admin/releases/${encodeURIComponent(operation.release.id)}/${operation.operation}`,
        {method: "POST", body: JSON.stringify({reason})}
      );
      message.success(
        operation.operation === "validate"
          ? "发布前校验通过，版本已进入 READY。"
          : operation.operation === "publish"
            ? "版本已正式发布并进入公开更新目录。"
            : operation.operation === "rollback"
              ? "版本已回滚，后续更新检查将不再选择它。"
              : "版本已归档，元数据和审计历史仍完整保留。"
      );
      setOperation(null);
      if (detail?.id === updated.id) setDetail(updated);
      await query.refetch();
    } catch (error) {
      message.error(errorMessage(error));
    } finally {
      setWorking(false);
    }
  };

  const columns: TableColumnsType<Release> = [
    {
      title: "版本",
      dataIndex: "version",
      render: (value: string, item) => (
        <div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.platform}/{item.architecture} · {item.channel}</span>
        </div>
      )
    },
    {
      title: "状态",
      dataIndex: "status",
      width: 140,
      render: (value: Release["status"]) => <Tag color={releaseColor[value]}>{value}</Tag>
    },
    {
      title: "最低兼容版本",
      dataIndex: "minimum_supported_version",
      width: 160
    },
    {
      title: "策略",
      key: "policy",
      width: 130,
      render: (_, item) => item.force_update ? <Tag color="red">强制更新</Tag> : <Tag>可选更新</Tag>
    },
    {title: "文件", dataIndex: "files", width: 90, render: (files: ReleaseSourceFile[]) => files.length},
    {
      title: "发布时间",
      dataIndex: "published_at",
      width: 180,
      render: (value: string | null) => value ? formatTime(value) : "—"
    },
    {
      title: "操作",
      key: "actions",
      fixed: "right",
      width: 330,
      render: (_, item) => (
        <Space size={2}>
          <Button type="link" icon={<EyeOutlined />} onClick={() => showDetail(item)}>详情</Button>
          {permissions.includes("updates.create") && ["DRAFT", "READY"].includes(item.status) && (
            <Button type="link" icon={<CheckCircleOutlined />} onClick={() => setOperation({release: item, operation: "validate"})}>
              校验
            </Button>
          )}
          {permissions.includes("updates.publish") && item.status === "READY" && (
            <Button type="link" icon={<RocketOutlined />} onClick={() => setOperation({release: item, operation: "publish"})}>
              发布
            </Button>
          )}
          {permissions.includes("updates.rollback") && item.status === "PUBLISHED" && (
            <Button danger type="link" icon={<RollbackOutlined />} onClick={() => setOperation({release: item, operation: "rollback"})}>
              回滚
            </Button>
          )}
          {permissions.includes("updates.rollback") && ["DRAFT", "READY", "ROLLED_BACK"].includes(item.status) && (
            <Button type="link" icon={<InboxOutlined />} onClick={() => setOperation({release: item, operation: "archive"})}>
              归档
            </Button>
          )}
        </Space>
      )
    }
  ];

  return (
    <div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">RELEASES / CLIENT UPDATES</Typography.Text>
          <Typography.Title level={2}>客户端发布</Typography.Title>
          <Typography.Paragraph type="secondary">
            DRAFT → READY → PUBLISHED。正式发布与回滚均要求二次 MFA，Manifest 使用 Ed25519 签名。
          </Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>刷新</Button>
          {permissions.includes("updates.create") && (
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>新建版本</Button>
          )}
        </Space>
      </section>
      <Card className="table-card">
        <Table<Release>
          rowKey="id"
          columns={columns}
          dataSource={result.data}
          loading={query.isLoading}
          pagination={false}
          scroll={{x: 1300}}
          locale={{emptyText: "尚无管理侧客户端发布。"}}
        />
      </Card>

      <Modal
        open={createOpen}
        title="新建客户端发布"
        width={900}
        confirmLoading={working}
        onCancel={() => !working && setCreateOpen(false)}
        footer={[
          <Button key="cancel" onClick={() => setCreateOpen(false)} disabled={working}>取消</Button>,
          createStep > 0 && <Button key="previous" onClick={() => setCreateStep((value) => value - 1)} disabled={working}>上一步</Button>,
          createStep < 2
            ? <Button key="next" type="primary" onClick={nextStep}>下一步</Button>
            : <Button key="create" type="primary" loading={working} onClick={() => form.submit()}>创建 DRAFT</Button>
        ]}
        destroyOnHidden
      >
        <Steps
          current={createStep}
          items={[
            {title: "版本信息"},
            {title: "文件与 Manifest"},
            {title: "预览"}
          ]}
          style={{marginBottom: 24}}
        />
        <Form form={form} layout="vertical" requiredMark={false} onFinish={create}>
          <div style={{display: createStep === 0 ? "block" : "none"}}>
            <Space align="start" wrap>
              <Form.Item label="平台" name="platform" rules={[{required: true}]}>
                <Select style={{width: 180}} options={[
                  {label: "Windows", value: "windows"},
                  {label: "Linux", value: "linux"},
                  {label: "macOS", value: "macos"}
                ]} />
              </Form.Item>
              <Form.Item label="架构" name="architecture" rules={[{required: true}]}>
                <Select style={{width: 160}} options={[
                  {label: "amd64", value: "amd64"},
                  {label: "arm64", value: "arm64"}
                ]} />
              </Form.Item>
              <Form.Item label="渠道" name="channel" rules={[{required: true}]}>
                <Select style={{width: 140}} options={[
                  {label: "Stable", value: "stable"},
                  {label: "Beta", value: "beta"}
                ]} />
              </Form.Item>
            </Space>
            <Space align="start" wrap>
              <Form.Item label="版本" name="version" rules={[{required: true, whitespace: true}]}>
                <Input placeholder="1.4.0" />
              </Form.Item>
              <Form.Item label="最低兼容版本" name="minimum_supported_version" rules={[{required: true, whitespace: true}]}>
                <Input placeholder="1.2.0" />
              </Form.Item>
            </Space>
            <Form.Item name="force_update" valuePropName="checked">
              <Checkbox>强制旧版本更新（发布时最低兼容版本将提升到本版本）</Checkbox>
            </Form.Item>
          </div>

          <div style={{display: createStep === 1 ? "block" : "none"}}>
            <Alert
              type="info"
              showIcon
              message="选择已上传到对象存储的文件"
              description="当前管理 API 不接收任意二进制上传；请填写受控 CDN 下的 Object Key、真实大小与 SHA-256。校验会从服务端对每个对象执行 HEAD 可用性探测，失败时不能发布。"
              style={{marginBottom: 16}}
            />
            <Form.List name="files" rules={[{validator: async (_, files) => {
              if (!files?.length) throw new Error("至少添加一个文件。");
            }}]}>
              {(fields, {add, remove}, {errors}) => (
                <Space direction="vertical" style={{width: "100%"}}>
                  {fields.map((field, index) => (
                    <Card key={field.key} size="small" title={`文件 ${index + 1}`} extra={
                      fields.length > 1 && <Button danger type="text" icon={<MinusCircleOutlined />} onClick={() => remove(field.name)}>移除</Button>
                    }>
                      <Space align="start" wrap>
                        <Form.Item {...field} label="File ID" name={[field.name, "file_id"]} rules={[{required: true}]}>
                          <Input placeholder="client_windows_140" />
                        </Form.Item>
                        <Form.Item {...field} label="安装路径" name={[field.name, "path"]} rules={[{required: true}]}>
                          <Input placeholder="bin/game.exe" />
                        </Form.Item>
                        <Form.Item {...field} label="大小（字节）" name={[field.name, "size"]} rules={[{required: true}]}>
                          <InputNumber min={0} />
                        </Form.Item>
                        <Form.Item {...field} label="压缩" name={[field.name, "compression"]} rules={[{required: true}]}>
                          <Select style={{width: 120}} options={["none", "gzip", "zstd"].map((value) => ({label: value, value}))} />
                        </Form.Item>
                      </Space>
                      <Form.Item {...field} label="SHA-256" name={[field.name, "sha256"]} rules={[
                        {required: true},
                        {pattern: /^[0-9a-f]{64}$/, message: "请输入 64 位小写十六进制 SHA-256。"}
                      ]}>
                        <Input maxLength={64} />
                      </Form.Item>
                      <Form.Item {...field} label="Object Key" name={[field.name, "object_key"]} rules={[{required: true}]}>
                        <Input placeholder="stable/1.4.0/game.exe.zst" />
                      </Form.Item>
                    </Card>
                  ))}
                  <Button type="dashed" block icon={<PlusOutlined />} onClick={() => add({
                    file_id: "", path: "", size: 0, sha256: "", compression: "none", object_key: ""
                  })}>添加文件</Button>
                  <Form.ErrorList errors={errors} />
                </Space>
              )}
            </Form.List>
          </div>

          <div style={{display: createStep === 2 ? "block" : "none"}}>
            <Result
              status="info"
              title="创建后仍不会立即发布"
              subTitle="系统先创建 DRAFT；必须通过 Manifest、签名、SHA-256、路径和版本兼容校验成为 READY，之后再经二次 MFA 正式发布。"
            />
            <Form.Item label="创建原因" name="reason" rules={[
              {required: true, whitespace: true, message: "请填写发布工单或变更原因。"},
              {max: 500}
            ]}>
              <Input.TextArea rows={4} maxLength={500} showCount placeholder="例如：发布工单 REL-2026-071，Windows stable 1.4.0" />
            </Form.Item>
          </div>
        </Form>
      </Modal>

      <Drawer
        open={detail !== null}
        title={`发布详情 · ${detail?.version ?? ""}`}
        width={860}
        loading={detailLoading}
        onClose={() => setDetail(null)}
      >
        {detail && (
          <Space direction="vertical" size="large" style={{width: "100%"}}>
            <Descriptions bordered column={2} size="small">
              <Descriptions.Item label="状态"><Tag color={releaseColor[detail.status]}>{detail.status}</Tag></Descriptions.Item>
              <Descriptions.Item label="范围">{detail.platform}/{detail.architecture} · {detail.channel}</Descriptions.Item>
              <Descriptions.Item label="最低兼容">{detail.minimum_supported_version}</Descriptions.Item>
              <Descriptions.Item label="强制更新">{detail.force_update ? "是" : "否"}</Descriptions.Item>
              <Descriptions.Item label="Manifest Hash" span={2}>{detail.manifest?.manifest_hash ?? "尚未生成"}</Descriptions.Item>
              <Descriptions.Item label="签名 Key" span={2}>{detail.manifest ? `${detail.manifest.signature_algorithm} · ${detail.manifest.key_id}` : "尚未生成"}</Descriptions.Item>
            </Descriptions>
            <div>
              <Typography.Title level={4}>发布前检查</Typography.Title>
              <Space direction="vertical" style={{width: "100%"}}>
                {detail.validation.checks.map((check) => (
                  <Alert
                    key={check.key}
                    type={check.passed ? "success" : "error"}
                    showIcon
                    message={check.message}
                  />
                ))}
                {!detail.validation.checks.length && <Typography.Text type="secondary">尚未执行校验。</Typography.Text>}
              </Space>
            </div>
            <div>
              <Typography.Title level={4}>文件</Typography.Title>
              <Table<ReleaseSourceFile>
                rowKey="file_id"
                dataSource={detail.files}
                pagination={false}
                size="small"
                columns={[
                  {title: "路径", dataIndex: "path"},
                  {title: "大小", dataIndex: "size", width: 120, render: formatBytes},
                  {title: "压缩", dataIndex: "compression", width: 100},
                  {title: "Object Key", dataIndex: "object_key"}
                ]}
              />
            </div>
          </Space>
        )}
      </Drawer>

      <OperationReasonModal
        open={operation !== null}
        title={operationTitle(operation)}
        consequence={operationConsequence(operation)}
        confirmLabel={operation?.operation === "validate" ? "开始校验" : operation?.operation === "publish" ? "确认正式发布" : operation?.operation === "rollback" ? "确认回滚" : "确认归档"}
        danger={operation?.operation === "rollback" || operation?.operation === "publish"}
        requireMFA={operation?.operation === "publish" || operation?.operation === "rollback" || operation?.operation === "archive"}
        loading={working}
        onCancel={() => setOperation(null)}
        onConfirm={execute}
      />
    </div>
  );
}

const releaseColor: Record<Release["status"], string> = {
  DRAFT: "default",
  READY: "blue",
  PUBLISHED: "green",
  ROLLED_BACK: "orange",
  ARCHIVED: "default"
};

function operationTitle(operation: {release: Release; operation: ReleaseOperation} | null) {
  if (!operation) return "发布操作";
  if (operation.operation === "validate") return `校验 ${operation.release.version}？`;
  if (operation.operation === "publish") return `正式发布 ${operation.release.version}？`;
  if (operation.operation === "rollback") return `回滚 ${operation.release.version}？`;
  return `归档 ${operation.release.version}？`;
}

function operationConsequence(operation: {release: Release; operation: ReleaseOperation} | null) {
  if (operation?.operation === "validate") return "将重新生成并验证 Manifest、文件元数据、CDN 对象可访问性、兼容版本及 Ed25519 签名。";
  if (operation?.operation === "publish") return `版本将进入公开更新目录，影响 ${operation.release.platform}/${operation.release.architecture} ${operation.release.channel} 用户。`;
  if (operation?.operation === "rollback") return "版本将退出公开更新目录，客户端会回退选择该渠道中仍为 PUBLISHED 的上一版本。审计历史不会删除。";
  return "版本将从活跃发布流程中隐藏，但版本元数据、签名结果和审计历史都会保留。归档后不可恢复。";
}

function formatTime(value: string) {
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString("zh-CN");
}

function formatBytes(value: number) {
  if (value < 1024) return `${value} B`;
  if (value < 1024 * 1024) return `${(value / 1024).toFixed(1)} KiB`;
  return `${(value / 1024 / 1024).toFixed(1)} MiB`;
}

function errorMessage(error: unknown) {
  if (error instanceof ApiError) return error.requestId ? `${error.message}（请求编号：${error.requestId}）` : error.message;
  return error instanceof Error ? error.message : "操作失败。";
}
