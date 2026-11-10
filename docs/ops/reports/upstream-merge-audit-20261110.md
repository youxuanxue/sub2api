# Upstream merge audit — 2026-11-10

本报告固定同一组 Git 对象，记录 issue #3091 的可复核合并审计。它不执行合并、部署或线上写操作。

## 现网与 refs

| 对象 | 证据 |
|---|---|
| `origin/main` | `77f6f14977222171b5aba41ae99b8d078f01c42c`，版本 `1.8.359` |
| `upstream/main` | `e2c9812190dd71bb309c8ad020a1211da1e0d3b4`，相对 origin/main 领先 146 个提交 |
| merge-base | `25608d216bb7be04e4a646830cb065cd09b7d7e9` |
| prod | SSM `mi-080ed76dc6d97d8b5`，active blue，`1.8.359`，revision `77f6f14977222171b5aba41ae99b8d078f01c42c` |
| cn-prod | SSM `mi-06568963b4b3c3239`，active blue，`1.8.358`，revision `fd85e017f17def83bc975d7c44a83d3a3cc73c2c` |

线上 revision 通过 `run-probe.sh` 只读 SSM + Docker inspect 取得；未执行写操作。

## 结论

三方 `merge-tree --write-tree --messages --merge-base 25608d216bb7be04e4a646830cb065cd09b7d7e9 77f6f14977222171b5aba41ae99b8d078f01c42c e2c9812190dd71bb309c8ad020a1211da1e0d3b4` 产生 **99 个冲突路径**。冲突覆盖 Ent 生成物、gateway/调度/计费/账号、OpenAI/Codex、Antigravity/Gemini、apicompat、模型价格和管理端 UI；不能在单个 PR 中安全盲解。当前分支保持 origin/main，不含 merge commit。

## 精确冲突路径

- `.gitignore`
- `README_CN.md`
- `backend/cmd/server/VERSION`
- `backend/ent/migrate/schema.go`
- `backend/ent/mutation.go`
- `backend/ent/runtime/runtime.go`
- `backend/ent/usagelog.go`
- `backend/go.mod`
- `backend/go.sum`
- `backend/internal/config/config.go`
- `backend/internal/config/config_test.go`
- `backend/internal/handler/admin/channel_handler.go`
- `backend/internal/handler/composite_platform.go`
- `backend/internal/handler/composite_platform_test.go`
- `backend/internal/handler/gateway_handler.go`
- `backend/internal/handler/gateway_handler_chat_completions.go`
- `backend/internal/handler/gateway_handler_responses.go`
- `backend/internal/handler/gemini_v1beta_handler.go`
- `backend/internal/handler/grok_media.go`
- `backend/internal/handler/openai_gateway_handler.go`
- `backend/internal/handler/openai_images.go`
- `backend/internal/pkg/antigravity/gemini_types.go`
- `backend/internal/pkg/antigravity/request_transformer.go`
- `backend/internal/pkg/antigravity/request_transformer_test.go`
- `backend/internal/pkg/apicompat/anthropic_to_responses.go`
- `backend/internal/pkg/apicompat/responses_stream_event_wire.go`
- `backend/internal/pkg/apicompat/responses_to_anthropic.go`
- `backend/internal/pkg/apicompat/responses_to_chatcompletions.go`
- `backend/internal/pkg/openai/constants.go`
- `backend/internal/repository/migrations_runner.go`
- `backend/internal/repository/usage_log_repo_insert.go`
- `backend/internal/repository/usage_log_repo_insert_shape_unit_test.go`
- `backend/internal/repository/usage_log_repo_query.go`
- `backend/internal/repository/usage_log_repo_request_type_test.go`
- `backend/internal/repository/usage_log_session_id_unit_test.go`
- `backend/internal/server/middleware/api_key_auth.go`
- `backend/internal/server/middleware/api_key_auth_google.go`
- `backend/internal/server/routes/gateway.go`
- `backend/internal/service/account_usage_service.go`
- `backend/internal/service/antigravity_gateway_compat.go`
- `backend/internal/service/antigravity_gateway_gemini.go`
- `backend/internal/service/antigravity_gateway_retry.go`
- `backend/internal/service/antigravity_gateway_service.go`
- `backend/internal/service/api_key_auth_cache_impl.go`
- `backend/internal/service/api_key_service.go`
- `backend/internal/service/api_key_service_cache_test.go`
- `backend/internal/service/auth_service.go`
- `backend/internal/service/billing_service.go`
- `backend/internal/service/channel_service.go`
- `backend/internal/service/gateway_claude_oauth_body.go`
- `backend/internal/service/gateway_scheduling.go`
- `backend/internal/service/gemini_chat_completions_compat_service.go`
- `backend/internal/service/gemini_messages_compat_service.go`
- `backend/internal/service/gemini_messages_compat_service_test.go`
- `backend/internal/service/gemini_native_response_signal_test.go`
- `backend/internal/service/model_pricing_resolver.go`
- `backend/internal/service/model_rate_limit.go`
- `backend/internal/service/openai_account_runtime_block_fastpath.go`
- `backend/internal/service/openai_account_scheduler.go`
- `backend/internal/service/openai_apikey_responses_probe.go`
- `backend/internal/service/openai_apikey_responses_probe_test.go`
- `backend/internal/service/openai_compact_body_signal.go`
- `backend/internal/service/openai_content_session_seed.go`
- `backend/internal/service/openai_cyber_session_block.go`
- `backend/internal/service/openai_cyber_session_block_test.go`
- `backend/internal/service/openai_gateway_chat_completions.go`
- `backend/internal/service/openai_gateway_chat_completions_raw.go`
- `backend/internal/service/openai_gateway_messages.go`
- `backend/internal/service/openai_gateway_messages_chat_fallback.go`
- `backend/internal/service/openai_gateway_request_body.go`
- `backend/internal/service/openai_gateway_responses_chat_fallback.go`
- `backend/internal/service/openai_gateway_service.go`
- `backend/internal/service/openai_gateway_upstream_errors.go`
- `backend/internal/service/openai_gateway_usage.go`
- `backend/internal/service/openai_images.go`
- `backend/internal/service/openai_images_direct.go`
- `backend/internal/service/openai_images_direct_test.go`
- `backend/internal/service/openai_images_model_test.go`
- `backend/internal/service/openai_images_responses.go`
- `backend/internal/service/openai_responses_tool_schema_test.go`
- `backend/internal/service/openai_ws_http_bridge.go`
- `backend/internal/service/openai_ws_v2/passthrough_relay.go`
- `backend/internal/service/pricing_service.go`
- `backend/internal/service/ratelimit_service.go`
- `backend/resources/model-pricing/model_prices_and_context_window.json`
- `tree.`
- `deploy/config.example.yaml`
- `frontend/src/components/account/AccountUsageCell.vue`
- `frontend/src/components/keys/UseKeyModal.vue`
- `frontend/src/components/keys/__tests__/UseKeyModal.spec.ts`
- `frontend/src/components/modelPlaza/__tests__/PlazaModelPricingTable.spec.ts`
- `frontend/src/composables/useModelWhitelist.ts`
- `frontend/src/utils/__tests__/ccswitchImport.spec.ts`
- `frontend/src/utils/ccswitchImport.ts`
- `frontend/src/views/admin/AccountsView.vue`
- `frontend/src/views/admin/ChannelsView.vue`
- `frontend/src/views/admin/SettingsView.vue`
- `frontend/src/views/user/KeysView.vue`
- `frontend/src/views/user/PaymentView.vue`

## Fork 删除不变量

- `.github/workflows/security-scan.yml`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/cmd/cleanup-ingress-reject-logs/README.md`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/cmd/cleanup-ingress-reject-logs/main.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/cmd/cleanup-ingress-reject-logs/main_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/gemini_cached_content_hardening_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/gemini_cached_content_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/gemini_image_generation_gate_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/gemini_quota_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/gemini_stream_client_abort_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/openai_apikey_responses_probe_verdict_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/openai_gateway_messages_anthropic_native.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/openai_subscription_long_context_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/opencode_unsupported_models_routing_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/payment_refund_pending_hold_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/single_account_retry_probe_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/internal/service/temp_unsched_scope_test.go`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `backend/scripts/finalize-ingress-reject-cleanup.sql`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `deploy/EDGE_SECURITY.md`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `docs/ASYNC_IMAGE_TASKS.md`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `docs/BATCH_IMAGE_MVP.md`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `docs/COMPOSITE_GROUPS.md`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `docs/channel-monitor-v2-safe-defaults.md`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `frontend/src/components/layout/__tests__/siteLogoSanitization.spec.ts`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）
- `frontend/src/features/channel-monitor-v2/__tests__/designSystem.structure.spec.ts`（fork 在共同祖先之后删除；合并时默认保持删除，除非能把上游必要行为迁移到现 owner）

这些删除涉及安全扫描、旧 ingress 清理命令、Gemini/OpenAI/Anthropic 专项测试、旧路由测试、支付 hold 测试、旧部署安全文档、异步图片 MVP、复合组文档及已退役的前端测试。不能用上游版本批量复活。

## 最小分批方案

1. **A — harness/生成物**：单独 worktree 固定上游 SHA；先处理 `.gitignore`、VERSION、`go.mod/go.sum`、Ent schema/runtime/usagelog/mutation、迁移 runner 和生成检查。禁止同时改 gateway 行为。
2. **B — 线上高价值行为**：按供应面拆 PR，每个 PR 只承载一条用户可验收行为：OpenAI/Codex（session seed、cooldown、UTF-8、image、WS）；Antigravity/Gemini（quota、tool media、schema、streaming）；账号/调度/计费（refresh、slot release、cache TTL、pricing）。每批用对应真实请求和账单幂等指标验收，再回填 merge harness。
3. **C — 低风险外围**：frontend/settings、文档、sponsors 和独立兼容修复可在单独 cherry-pick PR 处理；不得借此宣称 upstream/main 已合入。

## 可直接模拟 cherry-pick 的上游提交

以下提交以其父提交为三方基线，对 origin/main 做 `merge-tree` 模拟无文本冲突；仍需运行项目测试和 TokenKey sentinel，不能自动合入：

- `b2a695d61` fix(openai): normalize Codex Desktop page context
- `f2b0a32b1` fix(apicompat): preserve sealed function call metadata
- `e401c5805` fix(openai): keep Codex metadata events retry-safe
- `137316a14` fix(payment): configure EasyPay refund success codes per instance
- `e6c19082f` chore: update sponsors
- `0578fb160` fix(openai): stop native WS later-turn first-frame replay
- `1c83bb825` test(repository): assert the auth projection carries require_privacy_set
- `f654c23eb` perf(gateway): decide thinking-block filtering without decoding the body
- `a0b058a9e` fix(bedrock): validate EventStream frame lengths before allocating
- `2ce1849bd` fix(antigravity): scope INTERNAL 500 penalties to image models; make account tiers configurable
- `c35418032` fix(antigravity): preserve direct Gemini streaming responses
- `fa6f06510` fix(openai): let error passthrough rules override the built-in body-limit 413
- `210608151` fix(anthropic): omit beta query for Cloudflare REST messages
- `090c58f66` fix(gateway): honor account error policies in compatibility paths
- `92d35a694` fix(pricing): retry sync after catalog hash mismatch
- `cf1ba819b` fix(openai): preserve native Codex image error statuses
- `5965ac4e9` feat(settings): 系统设置保存按钮改为底部吸附浮标
- `bea67f412` fix(openai): use the official Codex descriptor for GPT-6.1 Terra
- `142d22cc4` fix(openai-ws): reconnect pooled OAuth sockets before search activation
- `390ba9905` fix(securityaudit): 审计任务入队后立即唤醒本进程 worker

其余上游提交至少有一个冲突路径，需按 A/B/C 方案人工裁决。

## 验证命令

```text
git fetch origin --tags
git fetch upstream --tags
bash scripts/upstream/check-drift.sh
node /Users/feng/.codex/skills/upstream-merge/scripts/audit.mjs --base 77f6f14977222171b5aba41ae99b8d078f01c42c --upstream e2c9812190dd71bb309c8ad020a1211da1e0d3b4 --head 77f6f14977222171b5aba41ae99b8d078f01c42c
git merge-tree --write-tree --messages --merge-base 25608d216bb7be04e4a646830cb065cd09b7d7e9 77f6f14977222171b5aba41ae99b8d078f01c42c e2c9812190dd71bb309c8ad020a1211da1e0d3b4
```

本轮未运行 `preflight.sh` 或 Go/前端测试，因为没有产生代码合并；任何后续 A/B/C PR 必须在其自身 worktree 通过对应门禁后再推送。
