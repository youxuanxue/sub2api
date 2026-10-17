---
status: shipped
approved_by: user
related_prs: ["https://github.com/youxuanxue/sub2api/pull/2690"]
---

# Cursor 能力退役

本次会话用户明确要求「Cursor 账号以及连接 Cursor 等功能……全部退役」，作为产品范围授权。

移除 Cursor 账号授权、导入、续期、原生 Agent 转发和 continuation；移除管理端连接/重连入口及用户侧 Cursor 接入指南。供应模型目录、运维预设、探针和专用测试随能力删除。普通编辑器光标、分页 cursor 与 Codex VSCode 客户端识别不属于该产品能力。

遗留账号通过共享账号边界拒绝调度和重新创建；生产账号按实际 ID 核对后停用，保留历史账单、用量与审计数据。数据库不删表、不硬删历史记录。代码回滚可恢复入口，账号恢复需独立明确操作。

验证：后端编译及相关协议/调度/刷新测试；前端类型检查、接入指南与账号操作测试；Playwright 验证管理端和公开指南入口消失；目录生成一致性和 preflight。上线与线上停用结果须按实际执行记录，不以本地实现代替。

后续维护以本文和 US-066 为准。旧专用设计、操作记录和 US-060 已移出工作树，历史可从 #2690 的合并提交及其父提交查阅；不从历史恢复授权、探针、mapping 或部署入口。原设计中仍有效的共享输出上限兼容契约见 [协议 SSOT](protocol-routing-ssot.md#output-limit-compatibility)。

开发工具的 `.cursor/` 配置、Codex 编辑器识别、分页/界面光标、Grok 自有的 Composer 别名以及其他供应商的 Muse 模型不属于本次退役。遗留账号拒绝逻辑和退役回归测试必须保留。原始用量与探针证据保留为历史，不能用作现行供应能力或恢复依据。
