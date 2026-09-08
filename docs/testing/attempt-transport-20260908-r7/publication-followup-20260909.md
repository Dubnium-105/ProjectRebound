# Publication follow-up — 2026-09-09

GitHub 清除申请已提交：[工单 #4739985](https://support.github.com/ticket/personal/0/4739985)，提交时状态为 `open`。未附数据库文件、数据行或凭据。GitHub 是否完成缓存和历史对象清除仍待回复；此前的未发送草稿与纠正回执保留为当时的历史记录。

主仓库 r7 文档提交 `f3a291dedb9fca3d56247a06770b87f2ac7aae8d` 已普通推送。Toolbox 的原始测试提交 `43a4c82fe781ed67df6b4f4f82d1ff976ad586be` 保留在本地；其历史夹带临时构建产物，因此没有直接推送该历史。

Toolbox 已从远端基线 `63e5ee598b860b8f5d53dd147d436921ad290098` 发布[干净提交 `f69d567`](https://github.com/STanJK/ProjectReboundToolbox/commit/f69d56741208263c972309137e462a251b3b0eba)，分支为 `codex/authoritative-only-20260907`。纳入 109 个源码和必要资源路径，逐文件确认与原始测试提交的 Git blob 字节一致，排除 10,833 个临时或构建路径。MetaTunnel 嵌入式运行时属于必要资源，已核对其 manifest 尺寸和 SHA。测试中使用的合成凭据样例保留，未发现真实运行凭据。

r7 测试包字节和后端部署不变。此次仅整理公开提交及记录申请状态，没有重新构建或声称新增实机验收。三机原生准入与完整可玩对局仍未验收。

详见[状态回执](publication-followup-20260909.json)。请使用上面的干净公开提交继续协作，避免把本地旧历史合并回公开分支。
