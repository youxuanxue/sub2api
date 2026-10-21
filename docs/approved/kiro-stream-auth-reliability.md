---
title: Kiro 流、规则与刷新可靠性修复
status: approved
approved_by: "feng（本次对话：同意。请继续修复。）"
approved_at: 2026-10-21
---

# Kiro 流、规则与刷新可靠性修复

## 范围与依据

用户确认采纳[核实报告](../kiro-open-source-validation-20261021.md)中的五项修复：异常流、顶层 system 规则丢失、工具 ID 迟到、OAuth 刷新、region 地址校验。此次审批覆盖实现、验证与既有 #2764 的修复交付；不代表合并、部署或生产配置授权。EU 数据区域适配缺少真实账号验证，单独排期；不改调度、计费、Tokensea priority 或既有客户端工具执行边界。

## 行为契约

- EventStream：对长度、CRC、事件 JSON、原始 UTF-8/孤立 UTF-16 转义 与终止后的内容作校验；损坏帧返回错误，不产出成功完成。合法重复文本逐字保留。终止后仍允许用量事件，不允许新增内容/工具或第二个终止。合法终止后的干净 EOF 才是完成；截断尾帧不能伪装成功。限制单帧分配大小；无已输出重放。
- 工具：仅缓存唯一未识别工具，真实 ID 到达且完整合法 JSON 完成后才向客户端交付；歧义匿名并发、身份变化、重复完成 ID 或缺失 ID 拒绝执行。没有伪造 ID 或工具结果。
- system：取消基于关键词命中后整段替换；只替换已知 Claude Code 身份声明，保留任务规则、工具说明、项目上下文与自定义追加内容。未知模板保留而非猜测删除。验证真实上游拒答和工具循环；样本通过不代表所有模型无拒答。
- OAuth：请求携带取消上下文，异常 access/有效期不保存；上游省略 refresh token 时保留旧值，合法轮换交由现有 OAuthRefreshAPI 锁和持久化 owner 保存。profile 查询不得私自刷新并丢弃 token。
- region：OIDC 区域仅允许合法 AWS 区域形状，拒绝 authority/path/query/control 字符；凭据请求不跟随重定向。错误文本不携带上游响应中的凭据。认证区不用于改写生成数据区。

## 实现与验收

继续使用既有 translator、tool decoder、KiroTokenRefresher 与 OAuthRefreshAPI，无新增 DB 字段、用户 API 或 UI。测试覆盖正负向、真实 Forward 流式/非流式结构、无重放、凭据合并/取消/重定向、profile 旁路禁止刷新。Web impact: none。
