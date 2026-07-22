# 文档与本地化标准

[English](documentation-standard.md) | 简体中文

ProjectRebound 的公开文档同时维护英文和简体中文。英文是 GitHub 默认文件；简体中文同级文件使用 BCP 47 后缀 `.zh-CN.md`。

该结构结合了两个成熟开源项目的惯例：Kubernetes 使用标准语言代码和独立本地化内容，Ant Design 在 README 顶部提供直接的 `English / 中文` 切换。参考 [Kubernetes 本地化指南](https://kubernetes.io/docs/contribute/localization/)和 [Ant Design 仓库](https://github.com/ant-design/ant-design)。

## 文件命名

| 英文默认文件 | 简体中文文件 |
| --- | --- |
| `README.md` | `README.zh-CN.md` |
| `overview.md` | `overview.zh-CN.md` |
| `relay-continuity.md` | `relay-continuity.zh-CN.md` |

不得使用 `_cn`、`-cn`、`.zh` 或混合语言后缀。OpenAPI、protobuf、SQL migration、JSON、YAML 等与语言无关的机器契约保持单一来源。

## 语言切换

每对文档都在 H1 标题正下方放置切换入口：

```markdown
English | [简体中文](document.zh-CN.md)
```

```markdown
[English](document.md) | 简体中文
```

内部链接必须保持语言：英文索引链接英文文档；已有翻译时，中文索引链接 `.zh-CN.md` 文档。

## 内容一致性

- 两种语言必须描述相同的受支持行为、安全边界、命令、API 版本和发布状态。
- 代码块、端点路径、配置键、标识符、镜像标签、单位和验收阈值必须一致。
- 规范性变更必须在同一个 Pull Request 中同时更新两种语言；纯翻译措辞优化可以单独提交。
- 发生文字冲突时以英文人类可读文档为准；机器契约和实现测试优先于两种语言。
- 不翻译密钥、主机专用凭据、事故 Token 或历史证据数值。

## 范围与例外

仓库内每份当前维护的 Markdown 文档都由 CI 自动发现并检查，包括组件 README、邻近实现的协议说明和测试指南。新增英文文档时必须在同一提交中创建 `.zh-CN.md` 文件，不再需要维护登记清单。

`docs/archive/` 下的历史快照保持冻结，不做翻译。非 Markdown、与语言无关的机器契约保持单一来源。生成的依赖目录和构建缓存目录不属于维护文档范围。

## 审查清单

1. 同时更新英文和中文文件。
2. 检查语言切换与语言保持链接。
3. 对比命令、路径、版本、数值、表格、警告和发布状态。
4. 运行 `python Tools/Docs/check_markdown_links.py` 和 `python Tools/Docs/check_bilingual_docs.py`。
5. 将缺失或过期翻译视为发布文档缺陷。
