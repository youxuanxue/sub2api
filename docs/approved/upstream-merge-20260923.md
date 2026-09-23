---
title: TokenKey upstream merge 2026-09-23 approval anchor
status: approved
approved_by: "用户（本会话明确批准继续合并）"
approved_at: 2026-09-23
created: 2026-09-23
owners: [tk-platform]
scope: "Wei-Shaw/sub2api upstream/main merge into TokenKey"
---

# TokenKey upstream merge 2026-09-23

本审批锚点覆盖 `Wei-Shaw/sub2api@a3eb7ef302961cba716dc78b39b93b60c467db0e` 合入
TokenKey 的合并提交。合并保留双亲历史，并保留 TokenKey 的候选资格、协议路由、计费和
OpsCleanup 数据生命周期 owner。

请求日志保留期固定为 1–90 天，禁止 0；请求日志删除唯一 owner 为 OpsCleanup，设置读取
失败或非法值时跳过删除。
