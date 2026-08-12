import { localeTag, localizeSystemText, tr } from "../i18n";
import { CheckCircleOutlined, EyeOutlined, InboxOutlined, MinusCircleOutlined, PlusOutlined, ReloadOutlined, RocketOutlined, RollbackOutlined } from "@ant-design/icons";
import { useList } from "@refinedev/core";
import { Alert, App, Button, Card, Checkbox, Descriptions, Drawer, Form, Input, InputNumber, Modal, Result, Select, Space, Steps, Table, Tag, Typography, type TableColumnsType } from "antd";
import { useState } from "react";
import { ApiError, apiRequest, authClient } from "../api/client";
import { OperationReasonModal } from "../components/OperationReasonModal";
import type { Release, ReleaseFile, ReleaseSourceFile } from "../types";
type CreateValues = {
    platform: string;
    architecture: string;
    channel: "stable" | "beta" | "toolbox";
    version: string;
    minimum_supported_version: string;
    force_update: boolean;
    files: ReleaseSourceFile[];
    reason: string;
};
type ReleaseOperation = "validate" | "publish" | "rollback" | "archive";
export function ReleasesPage() {
    const { message } = App.useApp();
    const [form] = Form.useForm<CreateValues>();
    const [createOpen, setCreateOpen] = useState(false);
    const [createStep, setCreateStep] = useState(0);
    const [storageFiles, setStorageFiles] = useState<ReleaseFile[]>([]);
    const [storageFilesLoading, setStorageFilesLoading] = useState(false);
    const [storageFilesError, setStorageFilesError] = useState("");
    const [detail, setDetail] = useState<Release | null>(null);
    const [detailLoading, setDetailLoading] = useState(false);
    const [operation, setOperation] = useState<{
        release: Release;
        operation: ReleaseOperation;
    } | null>(null);
    const [working, setWorking] = useState(false);
    const permissions = authClient.permissions();
    const { query, result } = useList<Release>({
        resource: "releases",
        pagination: { pageSize: 100 }
    });
    const selectedFiles = Form.useWatch("files", form) ?? [];
    const selectedObjectKeys = new Set(selectedFiles.map((file) => file?.object_key).filter(Boolean));
    const loadStorageFiles = async () => {
        setStorageFilesLoading(true);
        setStorageFilesError("");
        try {
            const data = await apiRequest<{ items: ReleaseFile[] }>("/v1/admin/release-files");
            const files = data.items
                .sort((left, right) => left.original_file_name.localeCompare(right.original_file_name, localeTag()));
            setStorageFiles(files);
        }
        catch (error) {
            setStorageFiles([]);
            setStorageFilesError(errorMessage(error));
        }
        finally {
            setStorageFilesLoading(false);
        }
    };
    const openCreate = () => {
        form.setFieldsValue({
            platform: "windows",
            architecture: "amd64",
            channel: "stable",
            version: "",
            minimum_supported_version: "",
            force_update: false,
            files: [{ file_id: "", path: "", size: 0, sha256: "", compression: "none", object_key: "" }],
            reason: ""
        });
        setCreateStep(0);
        setCreateOpen(true);
        void loadStorageFiles();
    };
    const selectStorageFile = (index: number, objectKey: string) => {
        const selected = storageFiles.find((file) => file.object_key === objectKey);
        if (!selected)
            return;
        const files = [...(form.getFieldValue("files") ?? [])];
        const current = files[index] ?? {} as ReleaseSourceFile;
        const previous = storageFiles.find((file) => file.object_key === current.object_key);
        files[index] = {
            ...current,
            file_id: !current.file_id || current.file_id === previous?.id ? selected.id : current.file_id,
            path: !current.path || current.path === previous?.original_file_name ? selected.original_file_name : current.path,
            size: selected.size_bytes,
            sha256: selected.sha256,
            object_key: selected.object_key
        };
        if (form.getFieldValue("channel") === "toolbox" &&
            selected.original_file_name.toLowerCase() !== "vnt-runtime-manifest.json") {
            const sidecar = storageFiles.find((file) => file.version_label === selected.version_label &&
                file.original_file_name.toLowerCase() === "vnt-runtime-manifest.json");
            if (sidecar && !files.some((file) => file.object_key === sidecar.object_key)) {
                files.push({
                    file_id: sidecar.id,
                    path: "vnt-runtime-manifest.json",
                    size: sidecar.size_bytes,
                    sha256: sidecar.sha256,
                    compression: "none",
                    object_key: sidecar.object_key
                });
            }
        }
        form.setFieldsValue({ files });
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
            message.success(tr("DRAFT \u53D1\u5E03\u7248\u672C\u5DF2\u521B\u5EFA\uFF0C\u8BF7\u6267\u884C\u53D1\u5E03\u524D\u6821\u9A8C\u3002"));
            setCreateOpen(false);
            await query.refetch();
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const showDetail = async (release: Release) => {
        setDetail(release);
        setDetailLoading(true);
        try {
            setDetail(await apiRequest<Release>(`/v1/admin/releases/${encodeURIComponent(release.id)}`));
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setDetailLoading(false);
        }
    };
    const execute = async ({ reason }: {
        reason: string;
    }) => {
        if (!operation)
            return;
        setWorking(true);
        try {
            const updated = await apiRequest<Release>(`/v1/admin/releases/${encodeURIComponent(operation.release.id)}/${operation.operation}`, { method: "POST", body: JSON.stringify({ reason }) });
            message.success(operation.operation === "validate"
                ? tr("\u53D1\u5E03\u524D\u6821\u9A8C\u901A\u8FC7\uFF0C\u7248\u672C\u5DF2\u8FDB\u5165 READY\u3002") : operation.operation === "publish"
                ? tr("\u7248\u672C\u5DF2\u6B63\u5F0F\u53D1\u5E03\u5E76\u8FDB\u5165\u516C\u5F00\u66F4\u65B0\u76EE\u5F55\u3002") : operation.operation === "rollback"
                ? tr("\u7248\u672C\u5DF2\u56DE\u6EDA\uFF0C\u540E\u7EED\u66F4\u65B0\u68C0\u67E5\u5C06\u4E0D\u518D\u9009\u62E9\u5B83\u3002") : tr("\u7248\u672C\u5DF2\u5F52\u6863\uFF0C\u5143\u6570\u636E\u548C\u5BA1\u8BA1\u5386\u53F2\u4ECD\u5B8C\u6574\u4FDD\u7559\u3002"));
            setOperation(null);
            if (detail?.id === updated.id)
                setDetail(updated);
            await query.refetch();
        }
        catch (error) {
            message.error(errorMessage(error));
        }
        finally {
            setWorking(false);
        }
    };
    const columns: TableColumnsType<Release> = [
        {
            title: tr("\u7248\u672C"),
            dataIndex: "version",
            render: (value: string, item) => (<div className="primary-cell">
          <strong>{value}</strong>
          <span>{item.platform}/{item.architecture} · {item.channel}</span>
        </div>)
        },
        {
            title: tr("\u72B6\u6001"),
            dataIndex: "status",
            width: 140,
            render: (value: Release["status"]) => <Tag color={releaseColor[value]}>{value}</Tag>
        },
        {
            title: tr("\u6700\u4F4E\u517C\u5BB9\u7248\u672C"),
            dataIndex: "minimum_supported_version",
            width: 160
        },
        {
            title: tr("\u7B56\u7565"),
            key: "policy",
            width: 130,
            render: (_, item) => item.force_update ? <Tag color="red">{tr("\u5F3A\u5236\u66F4\u65B0")}</Tag> : <Tag>{tr("\u53EF\u9009\u66F4\u65B0")}</Tag>
        },
        { title: tr("\u6587\u4EF6"), dataIndex: "files", width: 90, render: (files: ReleaseSourceFile[]) => files.length },
        {
            title: tr("\u53D1\u5E03\u65F6\u95F4"),
            dataIndex: "published_at",
            width: 180,
            render: (value: string | null) => value ? formatTime(value) : "—"
        },
        {
            title: tr("\u64CD\u4F5C"),
            key: "actions",
            fixed: "right",
            width: 330,
            render: (_, item) => (<Space size={2}>
          <Button type="link" icon={<EyeOutlined />} onClick={() => showDetail(item)}>{tr("\u8BE6\u60C5")}</Button>
          {permissions.includes("updates.create") && ["DRAFT", "READY"].includes(item.status) && (<Button type="link" icon={<CheckCircleOutlined />} onClick={() => setOperation({ release: item, operation: "validate" })}>{tr("\u6821\u9A8C")}</Button>)}
          {permissions.includes("updates.publish") && item.status === "READY" && (<Button type="link" icon={<RocketOutlined />} onClick={() => setOperation({ release: item, operation: "publish" })}>{tr("\u53D1\u5E03")}</Button>)}
          {permissions.includes("updates.rollback") && item.status === "PUBLISHED" && (<Button danger type="link" icon={<RollbackOutlined />} onClick={() => setOperation({ release: item, operation: "rollback" })}>{tr("\u56DE\u6EDA")}</Button>)}
          {permissions.includes("updates.rollback") && ["DRAFT", "READY", "ROLLED_BACK"].includes(item.status) && (<Button type="link" icon={<InboxOutlined />} onClick={() => setOperation({ release: item, operation: "archive" })}>{tr("\u5F52\u6863")}</Button>)}
        </Space>)
        }
    ];
    return (<div className="page-stack">
      <section className="page-heading">
        <div>
          <Typography.Text className="eyebrow">RELEASES / CLIENT UPDATES</Typography.Text>
          <Typography.Title level={2}>{tr("\u5BA2\u6237\u7AEF\u53D1\u5E03")}</Typography.Title>
          <Typography.Paragraph type="secondary">{tr("DRAFT \u2192 READY \u2192 PUBLISHED\u3002\u6B63\u5F0F\u53D1\u5E03\u4E0E\u56DE\u6EDA\u5747\u8981\u6C42\u4E8C\u6B21 MFA\uFF0CManifest \u4F7F\u7528 Ed25519 \u7B7E\u540D\u3002")}</Typography.Paragraph>
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => query.refetch()} loading={query.isFetching}>{tr("\u5237\u65B0")}</Button>
          {permissions.includes("updates.create") && (<Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>{tr("\u65B0\u5EFA\u7248\u672C")}</Button>)}
        </Space>
      </section>
      <Card className="table-card">
        <Table<Release> rowKey="id" columns={columns} dataSource={result.data} loading={query.isLoading} pagination={false} scroll={{ x: 1300 }} locale={{ emptyText: tr("\u5C1A\u65E0\u7BA1\u7406\u4FA7\u5BA2\u6237\u7AEF\u53D1\u5E03\u3002") }}/>
      </Card>

      <Modal open={createOpen} title={tr("\u65B0\u5EFA\u5BA2\u6237\u7AEF\u53D1\u5E03")} width={900} confirmLoading={working} onCancel={() => !working && setCreateOpen(false)} footer={[
            <Button key="cancel" onClick={() => setCreateOpen(false)} disabled={working}>{tr("\u53D6\u6D88")}</Button>,
            createStep > 0 && <Button key="previous" onClick={() => setCreateStep((value) => value - 1)} disabled={working}>{tr("\u4E0A\u4E00\u6B65")}</Button>,
            createStep < 2
                ? <Button key="next" type="primary" onClick={nextStep}>{tr("\u4E0B\u4E00\u6B65")}</Button>
                : <Button key="create" type="primary" loading={working} onClick={() => form.submit()}>{tr("\u521B\u5EFA DRAFT")}</Button>
        ]} destroyOnHidden>
        <Steps current={createStep} items={[
            { title: tr("\u7248\u672C\u4FE1\u606F") },
            { title: tr("\u6587\u4EF6\u4E0E Manifest") },
            { title: tr("\u9884\u89C8") }
        ]} style={{ marginBottom: 24 }}/>
        <Form form={form} layout="vertical" requiredMark={false} onFinish={create}>
          <div style={{ display: createStep === 0 ? "block" : "none" }}>
            <Space align="start" wrap>
              <Form.Item label={tr("\u5E73\u53F0")} name="platform" rules={[{ required: true }]}>
                <Select style={{ width: 180 }} options={[
            { label: "Windows", value: "windows" },
            { label: "Linux", value: "linux" },
            { label: "macOS", value: "macos" }
        ]}/>
              </Form.Item>
              <Form.Item label={tr("\u67B6\u6784")} name="architecture" rules={[{ required: true }]}>
                <Select style={{ width: 160 }} options={[
            { label: "amd64", value: "amd64" },
            { label: "arm64", value: "arm64" }
        ]}/>
              </Form.Item>
              <Form.Item label={tr("\u6E20\u9053")} name="channel" rules={[{ required: true }]}>
                <Select style={{ width: 140 }} options={[
            { label: "Stable", value: "stable" },
            { label: "Beta", value: "beta" },
            { label: "ToolBox", value: "toolbox" }
        ]}/>
              </Form.Item>
            </Space>
            <Space align="start" wrap>
              <Form.Item label={tr("\u7248\u672C")} name="version" rules={[{ required: true, whitespace: true }]}>
                <Input placeholder="1.4.0"/>
              </Form.Item>
              <Form.Item label={tr("\u6700\u4F4E\u517C\u5BB9\u7248\u672C")} name="minimum_supported_version" rules={[{ required: true, whitespace: true }]}>
                <Input placeholder="1.2.0"/>
              </Form.Item>
            </Space>
            <Form.Item name="force_update" valuePropName="checked">
              <Checkbox>{tr("\u5F3A\u5236\u65E7\u7248\u672C\u66F4\u65B0\uFF08\u53D1\u5E03\u65F6\u6700\u4F4E\u517C\u5BB9\u7248\u672C\u5C06\u63D0\u5347\u5230\u672C\u7248\u672C\uFF09")}</Checkbox>
            </Form.Item>
          </div>

          <div style={{ display: createStep === 1 ? "block" : "none" }}>
            <Alert type="info" showIcon message={tr("\u4ECE\u5BF9\u8C61\u5B58\u50A8\u9009\u62E9\u6587\u4EF6")} description={tr("\u4EC5\u663E\u793A\u901A\u8FC7\u4E0B\u8F7D\u7BA1\u7406\u4E0A\u4F20\u5E76\u5B8C\u6210\u670D\u52A1\u7AEF\u6821\u9A8C\u7684\u5BF9\u8C61\u3002\u9009\u62E9\u540E\u4F1A\u81EA\u52A8\u5E26\u5165 Object Key\u3001\u771F\u5B9E\u5927\u5C0F\u548C SHA-256\uFF1BToolBox \u53D1\u5E03\u4F1A\u81EA\u52A8\u9644\u52A0\u540C\u7248\u672C\u7684 vnt-runtime-manifest.json\uFF0C\u540E\u7AEF\u5C06\u4ECE\u4E2D\u8BFB\u53D6 VNT \u517C\u5BB9\u7248\u672C\u3002")} action={<Button size="small" icon={<ReloadOutlined />} loading={storageFilesLoading} onClick={() => void loadStorageFiles()}>{tr("\u5237\u65B0\u6587\u4EF6")}</Button>} style={{ marginBottom: 16 }}/>
            {storageFilesError && (
              <Alert type="error" showIcon message={tr("\u65E0\u6CD5\u8BFB\u53D6\u5BF9\u8C61\u5B58\u50A8\u6587\u4EF6")} description={storageFilesError} style={{ marginBottom: 16 }}/>
            )}
            {!storageFilesLoading && !storageFilesError && storageFiles.length === 0 && (
              <Alert type="warning" showIcon message={tr("\u5C1A\u65E0\u53EF\u9009\u6587\u4EF6")} description={tr("\u8BF7\u5148\u5728\u4E0B\u8F7D\u7BA1\u7406\u4E2D\u4E0A\u4F20\u6587\u4EF6\uFF0C\u5E76\u7B49\u5F85\u670D\u52A1\u7AEF\u6821\u9A8C\u5B8C\u6210\u3002")} style={{ marginBottom: 16 }}/>
            )}
            <Form.List name="files" rules={[{ validator: async (_, files) => {
                    if (!files?.length)
                        throw new Error(tr("\u81F3\u5C11\u6DFB\u52A0\u4E00\u4E2A\u6587\u4EF6\u3002"));
                } }]}>
              {(fields, { add, remove }, { errors }) => (<Space direction="vertical" style={{ width: "100%" }}>
                  {fields.map((field, index) => (<Card key={field.key} size="small" title={tr(`文件 ${index + 1}`)} extra={fields.length > 1 && <Button danger type="text" icon={<MinusCircleOutlined />} onClick={() => remove(field.name)}>{tr("\u79FB\u9664")}</Button>}>
                      <Form.Item {...field} label={tr("\u5BF9\u8C61\u5B58\u50A8\u6587\u4EF6")} name={[field.name, "object_key"]} rules={[{ required: true, message: tr("\u8BF7\u9009\u62E9\u5BF9\u8C61\u5B58\u50A8\u6587\u4EF6\u3002") }]}>
                        <Select showSearch loading={storageFilesLoading} placeholder={tr("\u9009\u62E9\u5DF2\u6821\u9A8C\u7684\u5BF9\u8C61\u5B58\u50A8\u6587\u4EF6")} optionFilterProp="label" onChange={(value) => selectStorageFile(index, value)} options={storageFiles.map((file) => ({
                            value: file.object_key,
                            label: `${file.original_file_name} \u00B7 ${file.version_label} \u00B7 ${formatBytes(file.size_bytes)} \u00B7 ${file.status}`,
                            disabled: selectedObjectKeys.has(file.object_key) && selectedFiles[index]?.object_key !== file.object_key
                        }))}/>
                      </Form.Item>
                      <Space align="start" wrap>
                        <Form.Item {...field} label="File ID" name={[field.name, "file_id"]} rules={[{ required: true }]}>
                          <Input placeholder="client_windows_140"/>
                        </Form.Item>
                        <Form.Item {...field} label={tr("\u5B89\u88C5\u8DEF\u5F84")} name={[field.name, "path"]} rules={[{ required: true }]}>
                          <Input placeholder="bin/game.exe"/>
                        </Form.Item>
                        <Form.Item {...field} label={tr("\u5927\u5C0F\uFF08\u5B57\u8282\uFF09")} name={[field.name, "size"]} rules={[{ required: true }]}>
                          <InputNumber min={0} readOnly controls={false}/>
                        </Form.Item>
                        <Form.Item {...field} label={tr("\u538B\u7F29")} name={[field.name, "compression"]} rules={[{ required: true }]}>
                          <Select style={{ width: 120 }} options={["none", "gzip", "zstd"].map((value) => ({ label: value, value }))}/>
                        </Form.Item>
                      </Space>
                      <Form.Item {...field} label="SHA-256" name={[field.name, "sha256"]} rules={[
                    { required: true },
                    { pattern: /^[0-9a-f]{64}$/, message: tr("\u8BF7\u8F93\u5165 64 \u4F4D\u5C0F\u5199\u5341\u516D\u8FDB\u5236 SHA-256\u3002") }
                ]}>
                        <Input maxLength={64} readOnly/>
                      </Form.Item>
                    </Card>))}
                  <Button type="dashed" block icon={<PlusOutlined />} onClick={() => add({
                file_id: "", path: "", size: 0, sha256: "", compression: "none", object_key: ""
            })}>{tr("\u6DFB\u52A0\u6587\u4EF6")}</Button>
                  <Form.ErrorList errors={errors}/>
                </Space>)}
            </Form.List>
          </div>

          <div style={{ display: createStep === 2 ? "block" : "none" }}>
            <Result status="info" title={tr("\u521B\u5EFA\u540E\u4ECD\u4E0D\u4F1A\u7ACB\u5373\u53D1\u5E03")} subTitle={tr("\u7CFB\u7EDF\u5148\u521B\u5EFA DRAFT\uFF1B\u5FC5\u987B\u901A\u8FC7 Manifest\u3001\u7B7E\u540D\u3001SHA-256\u3001\u8DEF\u5F84\u548C\u7248\u672C\u517C\u5BB9\u6821\u9A8C\u6210\u4E3A READY\uFF0C\u4E4B\u540E\u518D\u7ECF\u4E8C\u6B21 MFA \u6B63\u5F0F\u53D1\u5E03\u3002")}/>
            <Form.Item label={tr("\u521B\u5EFA\u539F\u56E0")} name="reason" rules={[
            { required: true, whitespace: true, message: tr("\u8BF7\u586B\u5199\u53D1\u5E03\u5DE5\u5355\u6216\u53D8\u66F4\u539F\u56E0\u3002") },
            { max: 500 }
        ]}>
              <Input.TextArea rows={4} maxLength={500} showCount placeholder={tr("\u4F8B\u5982\uFF1A\u53D1\u5E03\u5DE5\u5355 REL-2026-071\uFF0CWindows stable 1.4.0")}/>
            </Form.Item>
          </div>
        </Form>
      </Modal>

      <Drawer open={detail !== null} title={tr(`发布详情 · ${detail?.version ?? ""}`)} width={860} loading={detailLoading} onClose={() => setDetail(null)}>
        {detail && (<Space direction="vertical" size="large" style={{ width: "100%" }}>
            <Descriptions bordered column={2} size="small">
              <Descriptions.Item label={tr("\u72B6\u6001")}><Tag color={releaseColor[detail.status]}>{detail.status}</Tag></Descriptions.Item>
              <Descriptions.Item label={tr("\u8303\u56F4")}>{detail.platform}/{detail.architecture} · {detail.channel}</Descriptions.Item>
              <Descriptions.Item label={tr("\u6700\u4F4E\u517C\u5BB9")}>{detail.minimum_supported_version}</Descriptions.Item>
              <Descriptions.Item label={tr("\u5F3A\u5236\u66F4\u65B0")}>{detail.force_update ? tr("\u662F") : tr("\u5426")}</Descriptions.Item>
              <Descriptions.Item label="Manifest Hash" span={2}>{detail.manifest?.manifest_hash ?? tr("\u5C1A\u672A\u751F\u6210")}</Descriptions.Item>
              <Descriptions.Item label={tr("\u7B7E\u540D Key")} span={2}>{detail.manifest ? `${detail.manifest.signature_algorithm} · ${detail.manifest.key_id}` : tr("\u5C1A\u672A\u751F\u6210")}</Descriptions.Item>
              {detail.vnt_runtime && <Descriptions.Item label="VNT Runtime" span={2}>{`vnts ${detail.vnt_runtime.vnts_version} / wrapper ${detail.vnt_runtime.wrapper_version}`}</Descriptions.Item>}
            </Descriptions>
            <div>
              <Typography.Title level={4}>{tr("\u53D1\u5E03\u524D\u68C0\u67E5")}</Typography.Title>
              <Space direction="vertical" style={{ width: "100%" }}>
                {detail.validation.checks.map((check) => (<Alert key={check.key} type={check.passed ? "success" : "error"} showIcon message={localizeSystemText(check.message, "Release validation check result")}/>))}
                {!detail.validation.checks.length && <Typography.Text type="secondary">{tr("\u5C1A\u672A\u6267\u884C\u6821\u9A8C\u3002")}</Typography.Text>}
              </Space>
            </div>
            <div>
              <Typography.Title level={4}>{tr("\u6587\u4EF6")}</Typography.Title>
              <Table<ReleaseSourceFile> rowKey="file_id" dataSource={detail.files} pagination={false} size="small" columns={[
                { title: tr("\u8DEF\u5F84"), dataIndex: "path" },
                { title: tr("\u5927\u5C0F"), dataIndex: "size", width: 120, render: formatBytes },
                { title: tr("\u538B\u7F29"), dataIndex: "compression", width: 100 },
                { title: "Object Key", dataIndex: "object_key" }
            ]}/>
            </div>
          </Space>)}
      </Drawer>

      <OperationReasonModal open={operation !== null} title={operationTitle(operation)} consequence={operationConsequence(operation)} confirmLabel={operation?.operation === "validate" ? tr("\u5F00\u59CB\u6821\u9A8C") : operation?.operation === "publish" ? tr("\u786E\u8BA4\u6B63\u5F0F\u53D1\u5E03") : operation?.operation === "rollback" ? tr("\u786E\u8BA4\u56DE\u6EDA") : tr("\u786E\u8BA4\u5F52\u6863")} danger={operation?.operation === "rollback" || operation?.operation === "publish"} requireMFA={operation?.operation === "publish" || operation?.operation === "rollback" || operation?.operation === "archive"} loading={working} onCancel={() => setOperation(null)} onConfirm={execute}/>
    </div>);
}
const releaseColor: Record<Release["status"], string> = {
    DRAFT: "default",
    READY: "blue",
    PUBLISHED: "green",
    ROLLED_BACK: "orange",
    ARCHIVED: "default"
};
function operationTitle(operation: {
    release: Release;
    operation: ReleaseOperation;
} | null) {
    if (!operation)
        return tr("\u53D1\u5E03\u64CD\u4F5C");
    if (operation.operation === "validate")
        return tr(`校验 ${operation.release.version}？`);
    if (operation.operation === "publish")
        return tr(`正式发布 ${operation.release.version}？`);
    if (operation.operation === "rollback")
        return tr(`回滚 ${operation.release.version}？`);
    return tr(`归档 ${operation.release.version}？`);
}
function operationConsequence(operation: {
    release: Release;
    operation: ReleaseOperation;
} | null) {
    if (operation?.operation === "validate")
        return tr("\u5C06\u91CD\u65B0\u751F\u6210\u5E76\u9A8C\u8BC1 Manifest\u3001\u6587\u4EF6\u5143\u6570\u636E\u3001CDN \u5BF9\u8C61\u53EF\u8BBF\u95EE\u6027\u3001\u517C\u5BB9\u7248\u672C\u53CA Ed25519 \u7B7E\u540D\u3002");
    if (operation?.operation === "publish")
        return tr(`版本将进入公开更新目录，影响 ${operation.release.platform}/${operation.release.architecture} ${operation.release.channel} 用户。`);
    if (operation?.operation === "rollback")
        return tr("\u7248\u672C\u5C06\u9000\u51FA\u516C\u5F00\u66F4\u65B0\u76EE\u5F55\uFF0C\u5BA2\u6237\u7AEF\u4F1A\u56DE\u9000\u9009\u62E9\u8BE5\u6E20\u9053\u4E2D\u4ECD\u4E3A PUBLISHED \u7684\u4E0A\u4E00\u7248\u672C\u3002\u5BA1\u8BA1\u5386\u53F2\u4E0D\u4F1A\u5220\u9664\u3002");
    return tr("\u7248\u672C\u5C06\u4ECE\u6D3B\u8DC3\u53D1\u5E03\u6D41\u7A0B\u4E2D\u9690\u85CF\uFF0C\u4F46\u7248\u672C\u5143\u6570\u636E\u3001\u7B7E\u540D\u7ED3\u679C\u548C\u5BA1\u8BA1\u5386\u53F2\u90FD\u4F1A\u4FDD\u7559\u3002\u5F52\u6863\u540E\u4E0D\u53EF\u6062\u590D\u3002");
}
function formatTime(value: string) {
    const date = new Date(value);
    return Number.isNaN(date.getTime()) ? "—" : date.toLocaleString(localeTag());
}
function formatBytes(value: number) {
    if (value < 1024)
        return `${value} B`;
    if (value < 1024 * 1024)
        return `${(value / 1024).toFixed(1)} KiB`;
    return `${(value / 1024 / 1024).toFixed(1)} MiB`;
}
function errorMessage(error: unknown) {
    if (error instanceof ApiError)
        return error.requestId ? tr(`${error.message}（请求编号：${error.requestId}）`) : error.message;
    return error instanceof Error ? error.message : tr("\u64CD\u4F5C\u5931\u8D25\u3002");
}
