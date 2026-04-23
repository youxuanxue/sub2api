# User Stories Index


| ID     | Title                                            | Status | Path                                                                                  |
| ------ | ------------------------------------------------ | ------ | ------------------------------------------------------------------------------------- |
| US-001 | Channel type bridge dispatch baseline            | Done   | `.testing/user-stories/stories/US-001-channel-type-bridge-dispatch-baseline.md`       |
| US-002 | OpenAI entrypoints affinity prefetch integration | Done   | `.testing/user-stories/stories/US-002-openai-affinity-entrypoints.md`                 |
| US-003 | Gateway responses/chat affinity integration      | Done   | `.testing/user-stories/stories/US-003-gateway-responses-chat-affinity-entrypoints.md` |
| US-004 | Bridge emergency kill switch and runtime counters | Done   | `.testing/user-stories/stories/US-004-bridge-killswitch-runtime-counters.md`          |
| US-005 | Preserve newapi core while converging peripheral TK features to upstream | Done   | `.testing/user-stories/stories/US-005-newapi-openai-compat-and-upstream-payment-gate.md` |
| US-006 | Upstream prompt-cache 粘性路由（统一注入） | InTest | `.testing/user-stories/stories/US-006-sticky-routing-prompt-cache.md` |
| US-007 | 重新引入上游 Backend Mode 并 fresh-install 默认开启 | InTest | `.testing/user-stories/stories/US-007-readopt-backend-mode-default-true.md` |
| US-008 | newapi group + `/v1/chat/completions` 端到端走通 | Draft  | `.testing/user-stories/stories/US-008-newapi-group-chat-completions-e2e.md` |
| US-009 | newapi group + `/v1/messages`（一站式 K）端到端走通 | Draft  | `.testing/user-stories/stories/US-009-newapi-group-messages-e2e.md` |
| US-010 | newapi group + `/v1/responses` 端到端走通 | Draft  | `.testing/user-stories/stories/US-010-newapi-group-responses-e2e.md` |
| US-011 | openai group 调度池不被 newapi 账号污染（混池漏洞防御） | InTest | `.testing/user-stories/stories/US-011-openai-pool-not-polluted-by-newapi.md` |
| US-012 | newapi group 池空时返回明确错误，channel_type=0 不入池 | InTest | `.testing/user-stories/stories/US-012-newapi-pool-empty-clear-error.md` |
| US-013 | newapi group + sticky session 命中 recheck / 漂移降级 | InTest | `.testing/user-stories/stories/US-013-newapi-group-sticky-session.md` |
| US-014 | newapi group 配置 messages_dispatch_model_config 持久化 | InTest | `.testing/user-stories/stories/US-014-newapi-group-messages-dispatch-config.md` |
| US-015 | 历史 openai group 行为完全不变（回归基线） | InTest | `.testing/user-stories/stories/US-015-openai-group-regression-baseline.md` |
| US-016 | SMTP EHLO host 从 From/Username 推导（修 Google Workspace `auth: EOF`） | Done   | `.testing/user-stories/stories/US-016-smtp-ehlo-host-from-config.md` |
| US-017 | Turnstile siteverify 失败可观测 + UX 自救引导 | InTest | `.testing/user-stories/stories/US-017-turnstile-observability-and-stale-tab-ux.md` |
| US-018 | Admin UI 接入第五平台 newapi（端到端可创建组与账号） | Draft  | `.testing/user-stories/stories/US-018-admin-ui-newapi-platform-end-to-end.md` |
| US-019 | newapi 账号暴露 model_mapping / status_code_mapping / openai_organization 三个真实影响转发的字段 | InTest | `.testing/user-stories/stories/US-019-newapi-forwarding-affecting-fields.md` |
| US-020 | 调度快照重建必须包含第五平台 newapi（防 PlatformNewAPI 漂移性丢失） | InTest | `.testing/user-stories/stories/US-020-newapi-scheduler-snapshot-includes-fifth-platform.md` |
| US-021 | newapi 账号保存时自动解析 Moonshot 区域 base URL（.cn vs .ai） | InTest | `.testing/user-stories/stories/US-021-newapi-moonshot-regional-resolve-on-save.md` |
| US-022 | NewAPI 第五平台 admin/HTTP 生命周期 audit 缺口修复（group binding / simple-mode seed / test-connection / available-models / chat 错误透传） | Done | `.testing/user-stories/stories/US-022-newapi-admin-lifecycle-audit-fixes.md` |
| US-023 | NewAPI 第五平台 runtime 路径 audit round 2 修复（429 OpenAI body 解析 / ops retry chat-completions 分类 + 守护） | Done | `.testing/user-stories/stories/US-023-newapi-runtime-path-audit-round-2.md` |
| US-024 | NewAPI 第五平台 round-3 audit 修复（BatchCreate 透传 channel_type/load_factor + ops openai-token-stats gpt% 过滤放行 newapi） | Done | `.testing/user-stories/stories/US-024-newapi-batch-import-and-token-stats-round-3.md` |
| US-025 | NewAPI 第五平台 round-4 audit 修复（sticky 会话指向已删除账号时主动清理 Redis 绑定） | Done | `.testing/user-stories/stories/US-025-newapi-sticky-session-deleted-account-cleanup.md` |
| US-026 | NewAPI 第五平台 round-5 audit 修复（admin SubscriptionsView 平台筛选下拉漏掉 newapi） | Done | `.testing/user-stories/stories/US-026-newapi-subscriptions-platform-filter.md` |
| US-027 | OpenAI Codex 伪装 Claude — Thinking 多轮空响应根治 + 流式空内容护栏 | InTest | `.testing/user-stories/stories/US-027-openai-codex-as-claude-thinking-continuity.md` |
| US-028 | NewAPI bridge 错误回灌 RateLimitService + handle401/handle402 跨 OpenAI-compat 平台 + relay-error JSON status 兜底 (Bug B-1 / B-3 / B-11) | InTest | `.testing/user-stories/stories/US-028-newapi-bridge-error-pipeline.md` |
| US-029 | Admin BulkUpdate 拒绝 newapi credentials + BatchUpdateCredentials 走专用 writer 跳过 Moonshot 探测 (Bug B-4 / B-5) | InTest | `.testing/user-stories/stories/US-029-admin-bulk-and-batch-credentials-newapi.md` |
| US-030 | tryStickySessionHit 在 bound account 不可调度 / 跨池漂移时主动清理 Redis 绑定 (Bug B-7) | InTest | `.testing/user-stories/stories/US-030-sticky-session-unschedulable-clears.md` |
| US-031 | NewAPI bridge & handler 契约清理 — sticky 注入器拆分 / 双写防护 / TTL 漏斗 / selection-failure 显式 return (Bug B-2 / B-6 / B-8 / B-10) | InTest | `.testing/user-stories/stories/US-031-newapi-bridge-and-handler-contract-cleanup.md` |
