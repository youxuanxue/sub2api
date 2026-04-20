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
| US-008 | newapi group + `/v1/chat/completions` 端到端走通 | Draft | `.testing/user-stories/stories/US-008-newapi-group-chat-completions-e2e.md` |
| US-009 | newapi group + `/v1/messages`（一站式 K）端到端走通 | Draft | `.testing/user-stories/stories/US-009-newapi-group-messages-e2e.md` |
| US-010 | newapi group + `/v1/responses` 端到端走通 | Draft | `.testing/user-stories/stories/US-010-newapi-group-responses-e2e.md` |
| US-011 | openai group 调度池不被 newapi 账号污染（混池漏洞防御） | Draft | `.testing/user-stories/stories/US-011-openai-pool-not-polluted-by-newapi.md` |
| US-012 | newapi group 池空时返回明确错误，channel_type=0 不入池 | Draft | `.testing/user-stories/stories/US-012-newapi-pool-empty-clear-error.md` |
| US-013 | newapi group + sticky session 命中 recheck / 漂移降级 | Draft | `.testing/user-stories/stories/US-013-newapi-group-sticky-session.md` |
| US-014 | newapi group 配置 messages_dispatch_model_config 持久化 | Draft | `.testing/user-stories/stories/US-014-newapi-group-messages-dispatch-config.md` |
| US-015 | 历史 openai group 行为完全不变（回归基线） | Draft | `.testing/user-stories/stories/US-015-openai-group-regression-baseline.md` |
