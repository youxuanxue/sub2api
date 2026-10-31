---
title: API Key 原地轮换与工单飞书通知
status: approved
approved_by: "用户（本会话：批准、开始实现；轮换语义确认）"
authors: [agent]
---

# API Key 原地轮换与工单飞书通知

## 决策

API Key 操作栏新增“轮换”（英文文案 `revoke`）操作。操作在原 `api_keys` 记录上只更新 `key` 值，保留 ID、名称、分组、路由、权限、额度、限额、IP 规则、过期时间和状态；旧值立即失效。现有禁用和删除语义不变。

系统新增简易工单接口和页面。工单数据、状态和权限以系统数据库为准；创建工单和管理员更新状态时，按系统已配置的飞书 Webhook 发送脱敏通知。飞书发送失败不回滚工单写入，也不引入额外重试状态机。

## 接口与状态

- 用户：`POST/GET /api/v1/feedback`、`GET /api/v1/feedback/:id`
- 管理员：`GET /api/v1/admin/feedback`、`PATCH /api/v1/admin/feedback/:id`
- 工单状态：`open`、`processing`、`resolved`、`closed`
- 工单按用户隔离；管理员可查看并更新状态。

## 安全边界

轮换只允许 API Key 所属用户执行，服务端使用列掩码只更新凭证列，并清理旧值和新值的认证缓存。工单通知会遮蔽 API Key、Bearer、Cookie、密码、Secret 等凭证样式内容，并复用现有飞书签名发送器。

## 验证

专项单元测试覆盖轮换保留原记录、越权拒绝、写失败保留旧值，以及工单通知脱敏和飞书配置读取。后端运行 `go test -tags unit` 相关包；前端运行 `npm run typecheck` 和 Vitest（依赖安装后）。

上游合并保护：轮换请求及其幂等 header 由 `scripts/sentinels/frontend-tk.json` 锚定；后端 wire/service 变更已完成人工 sentinel registry 复审。
