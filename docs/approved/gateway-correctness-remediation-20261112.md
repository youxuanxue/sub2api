---
title: 网关错误与局部安全修复
status: approved
approved_by: user (conversation confirmation and scope reduction 2026-11-12)
created: 2026-11-12
authors: [codex]
risk: high
---

# 网关错误与局部安全修复

## Scope

原始任务是核实 issue 对 TokenKey 的影响，优先修复已证实、有实际价值的问题，
不是重建调度、统计或登录系统。2026-11-12 用户要求回到原始问题与目标，
砍掉过度设计；本修订取代同日此前扩大后的实现方案，不授权合并或部署。

| 最初问题 | 目标 | 本轮处理 |
| --- | --- | --- |
| NewAPI Messages 长请求最终 502 | 区分调用方取消与真实上游失败 | 复用已合入 #3161 的 499 owner，不另造实现 |
| Chat SSE HTTP 200 内含 error / 不完整 EOF | 失败不伪装成功；只在尚未输出业务内容时沿既有策略 failover；保留 usage | 保留共享检测和 raw / Responses / Messages / buffered 修补 |
| Codex 会话与上游错误摘要隔离 | 不同认证调用方不共享对话身份；不透出私有诊断 | 保留会话作用域与安全错误封装 |
| 刷新缓存失败仍继续签发 | 不向用户返回无法安全登记的新 token；保留多标签页体验 | 仅保留缓存写入失败即返回错误及尽力清理 |
| 日志凭证别名、prompt audit 原文出口 | 减少凭证和用户内容泄漏 | 保留集中脱敏和 audit 原文落库/API/UI 出口移除 |
| outbox 小 ID 晚提交遗漏、rollup 并发风险 | 确保调度更新和金额正确 | 本 PR 不解决；恢复原实现，风险保留为未解决 |

## Evidence Boundary

此前只读核验（2026-11-12 01:56 UTC）关联到至少一条 Messages 取消误记 502，
不证明所有 502 同因；#3161 已覆盖取消归类，不据此宣称线上已全部恢复。

两地 outbox 快照没有水位后的积压，核验时没有观察到活动 rollup 锁等待。
这不排除晚提交遗漏或汇总阻塞，但也不等于确认线上已发生。当前轮不以这些快照为
理由上线完整投递/统计重构，也不能把撤出实现的项目标记为已修复。

## Retained Contracts

- Chat 顶层非 null error 共享识别；role、空 content 和 usage-only 不算业务输出。
  已输出 text/tool 后不得重放请求；失败或不完整 EOF 不得伪装 completed。
  不扩大既有重试预算，保留 usage；正常终止行为保持不变。
- 错误清洗保留安全 type/code/message 与字符串或数字形式的
  `error.headers.retry-after`，不恢复其他 header 或嵌套诊断。
  内部分类保留明确的认证、权限、限流和容量状态，不新增对客诊断字段。
- Codex 会话身份区分认证 API key、实际凭证账号和客户端 session；
  同一作用域多轮稳定，保持既有 device/installation identity。
- 刷新沿用现有 10 秒宽限和多 successor 策略；不再要求同一旧 token 返回同一结果。
  标记轮转、写用户索引或家族索引失败时停止签发并尽力删除相关 token。
  清理也失败时仍返回错误，不承诺缓存故障期间原子撤销或所有残留立即消失。
  保持 active/version/binding 校验和既有并发体验。
- logredact 集中识别凭证的 snake/camel/kebab 别名；不承诺任意自然语言 DLP。
  prompt audit 不新增原文持久化，不从历史原文列读取 API/UI 内容；
  保留脱敏预览、hash 和判定，不清除历史数据，不改变 QA raw 生命周期。

## Removed Design

撤出 claim/lease/ACK、续租、投影 generation、连接保活、Redis WATCH 写入保护、
禁止 pending 删除的 trigger、dirty-day 表、汇总 REPEATABLE READ 扩展、
刷新 single-successor CAS 与加密结果重放，以及仅服务这些机制的接口、测试和哨兵。
删除未上线的 `tk_112` / `tk_113`，不删除已有测试或修改已上线 migration。

本 PR 不引入 schema 或 Redis 记录格式切换，因此撤销此前为这些重构设计的
停机迁移及特殊回退要求。正常发布仍走既有发布 owner；这里不是部署授权。

## Validation

- 流内错误：raw / Responses / Messages / buffered，输出前后，usage 保留，错误不完成。
- `retry-after`：安全清洗、JSON/SSE/WS 构造，字符串/数字及拒绝嵌套诊断。
- 刷新：缓存标记/索引失败，清理成功/失败均不返回 token；既有宽限、并发、
  禁用、改密与绑定回归原样保留。
- 脱敏与 audit：canary 不泄漏，正常字段保留；原文不落库/DTO/UI，真实浏览器验证。
- 确认调度、汇总实现及其原测试与 origin/main 一致，无新增 migration 或悬空引用。
- 修改后的验证须重新运行，旧提交 CI 全绿不作为新 tree 的通过证据。
