# User Stories Index



| ID     | Title                                            | Status | Path                                                                                  |
| ------ | ------------------------------------------------ | ------ | ------------------------------------------------------------------------------------- |
| US-051 | Prod component release | Done | `.testing/user-stories/stories/US-051-prod-component-release.md` |
| US-050 | Candidate Eligibility SSOT | Done | `.testing/user-stories/stories/US-050-candidate-eligibility-ssot.md` |
| US-006 | Upstream prompt-cache 粘性路由（统一注入） | InTest | `.testing/user-stories/stories/US-006-sticky-routing-prompt-cache.md` |
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
| US-027 | OpenAI Codex 伪装 Claude — Thinking 多轮空响应根治 + 流式空内容护栏 | Done | `.testing/user-stories/stories/US-027-openai-codex-as-claude-thinking-continuity.md` |
| US-028 | 公开模型 + 价格目录页（未登录可访） | InTest | `.testing/user-stories/stories/US-028-public-pricing-catalog.md` |
| US-029 | 注册赠额（默认 $1.00 USD，admin 可配；3 条注册路径全覆盖） | InTest | `.testing/user-stories/stories/US-029-signup-bonus-balance.md` |
| US-030 | 注册成功自动签发 trial API Key（Dashboard Quick Start 卡片 v1.5 deferred） | InTest | `.testing/user-stories/stories/US-030-auto-first-api-key.md` |
| US-031 | Onboarding Tour 对普通用户开放（服务端字段记忆已看过） | InTest | `.testing/user-stories/stories/US-031-onboarding-tour-unlock-for-regular-users.md` |
| US-034 | OpenAI-compat `/v1/messages` 大输入自动压缩（账号优先、分组兜底） | InTest | `.testing/user-stories/stories/US-034-messages-compaction-policy.md` |
| US-035 | Model surface bundle activation 与 generic deploy 解耦 | InTest | `.testing/user-stories/stories/US-035-model-surface-bundle-activation.md` |
| US-038 | Kiro CONTENT_FILTERED 返回客户端错误且不触发 failover | InTest | `.testing/user-stories/stories/US-038-kiro-content-filter-client-error.md` |
| US-041 | Kiro Claude Code completion continuity preserves unfinished work | InTest | `.testing/user-stories/stories/US-041-kiro-claude-code-completion-continuity.md` |
| US-046 | 默认 API Key 的诚实能力发现与统一菜单 | Done | `.testing/user-stories/stories/US-046-universal-key-capability-discovery.md` |
| US-047 | Anthropic 缓冲流失败进入 failover，部分内容诚实标记截断 | Done | `.testing/user-stories/stories/US-047-anthropic-buffered-stream-failure-contract.md` |
| US-048 | 运营以供应源安全接入并热更新模型供应账号 | InTest | `.testing/user-stories/stories/US-048-model-supplier-source-management.md` |
| US-049 | Gateway failover 判定使用全局单一 owner | Done | `.testing/user-stories/stories/US-049-gateway-failover-policy-ssot.md` |
| US-052 | Gemini generateContent 使用通用 Chat 转换边 | InTest | `.testing/user-stories/stories/US-052-gemini-chat-conversion.md` |
| US-053 | 正文采集与入口安全边界 | InTest | `.testing/user-stories/stories/US-053-security-capture-and-ingress.md` |


| US-054 | Public Quickstart 与统一注册承诺 | Done | `.testing/user-stories/stories/US-054-public-quickstart-registration-offer.md` |

| US-056 | Gemini Web Cookie HTTP gateway | InTest | `.testing/user-stories/stories/US-056-gemini-web-http.md` |

| US-057 | Gemini Web 兼容入口复用原生协议 | InTest | `.testing/user-stories/stories/US-057-gemini-web-protocol-ssot.md` |

| US-058 | Studio 与 Quickstart 生图请求一致性 | Done | `.testing/user-stories/stories/US-058-image-generation-contract.md` |

| US-059 | Antigravity OpenAI Images 协议适配 | InTest | `.testing/user-stories/stories/US-059-antigravity-images-generations.md` |

| US-061 | 充值资格与 Studio 媒体错误提示 | Done | `.testing/user-stories/stories/US-061-trial-media-recharge.md` |

| US-062 | Claude context error recovery | Done | `.testing/user-stories/stories/US-062-claude-context-error-recovery.md` |

| US-063 | Hetzner 数据库备份安全切换（historical：Hetzner 已退役） | Done | `.testing/user-stories/stories/US-063-hcloud-pgdump-storage.md` |

| US-064 | OAuth 集中后的出口和错误语义 | InTest | `.testing/user-stories/stories/US-064-oauth-consolidation-gateway-safety.md` |

| US-065 | Kiro oversized requests preserve the current turn and fail clearly | InTest | `.testing/user-stories/stories/US-065-kiro-payload-overflow.md` |

| US-066 | 物理增量备份与恢复 | InTest | `.testing/user-stories/stories/US-066-physical-backup-recovery.md` |

| US-067 | OAuth 搜索历史补全保留工具权限 | InTest | `.testing/user-stories/stories/US-067-oauth-search-history-tool-permissions.md` |

| US-068 | cn-prod 私有镜像预下载 | InTest | `.testing/user-stories/stories/US-068-cn-image-prewarm.md` |

| US-070 | Kiro 文件工具按账号分块与轮次保真 | InTest | `.testing/user-stories/stories/US-070-kiro-file-tool-chunking.md` |

| US-071 | Kiro 协议与凭据可靠性 | InTest | `.testing/user-stories/stories/US-071-kiro-stream-auth-reliability.md` |

| US-072 | Kiro auto-mode transcript trim | Done | `.testing/user-stories/stories/US-072-kiro-auto-mode-transcript-trim.md` |

| US-073 | 用户历史用量账单查询与下载 | Done | `.testing/user-stories/stories/US-073-historical-usage-billing.md` |
