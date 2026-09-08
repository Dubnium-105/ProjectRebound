# 文件检查

[English](README.md) | 简体中文


从任何目录运行两个文档验证器：

```bash
python Tools/Docs/check_markdown_links.py
python Tools/Docs/check_bilingual_docs.py
```

`check_markdown_links.py`检查根英文/中文自述文件下的相关链接，`docs/`, `Backend/`, `Desktop/`， 和`Tools/`。故意不获取外部 URL 和页面片段。

`check_bilingual_docs.py` 自动验证仓库内每份维护中的 Markdown 文档。每份文档都必须具有 `.zh-CN.md` 同级文件、标准语言切换、相同的标题和代码块结构，以及保持当前语言的内部链接。`docs/archive/` 下的历史文件和生成的依赖/构建缓存目录不在检查范围内。`docs/implementation/strict-roster-20260907/` 与 `docs/testing/hardware-test-20260908/` 下的日期化验收快照也不在检查范围内：这些目录是不可改写的证据与契约快照，不是维护中的产品手册，因此保留其原始字节和当时记录的语言覆盖。
