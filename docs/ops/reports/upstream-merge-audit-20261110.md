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
- `deploy/EDGE_SECURITY.md`
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

## 后续合并的提交与验证方案

A/B/C 是通用 `upstream-merge` skill 定义的提交职责，不是按供应面拆分的 PR 批次。实际合并须在 `merge/upstream-*` 隔离分支固定本报告两端 SHA，保留完整上游历史；不能只合生成物后宣称完成合并。

1. **A — merge harness**：完整 `git merge --no-ff --no-commit`，裁决全部冲突、保留经 ledger 确认的删除，并同步依赖、schema/Ent/Wire 生成物和迁移。不能整文件取 upstream 覆盖 TokenKey 行为；提交前须满足项目门禁与基础编译。
2. **B — invariant repair**：按下表恢复或适配 TokenKey 不变量，保留调用入口、共享 owner 和行为测试；不能用修改 sentinel 清单来掩盖行为丢失。
3. **C — convergence**：仅收敛本次引入的分叉到现有 companion/facade/component，不夹带无关重构。

最终完整分支须固定 `.upstream-ref`，验证上游目标为 HEAD 祖先，运行 preflight、Go unit/integration、相关前端 lint/typecheck/build 与真实 UI Playwright 验收。按 OpenAI/Codex、Antigravity/Gemini、账号/调度/计费组织验证，不等同于拆断 merge 历史。紧急独立修复可以另开 cherry-pick PR，但仍需复核依赖与语义，也不代表完成 upstream merge。

## TokenKey 特性覆盖与现网影响复核（2026-11-11）

本 PR 相对固定 fork base 只增加此报告：没有删除或修改 TokenKey 运行代码、schema、配置、sentinel 或测试。合并本 PR 不会获得上游修复，也不会直接改变网关、账单、登录或页面显示；为这份文档单独部署没有功能收益。上表 prod/cn-prod revision 是 2026-11-10 的采集快照，不是本次复审重新探测的现网状态。

复核以 merge-base → upstream 的真实改动与固定 fork base 的既有实现对照。上游这段范围没有删除文件，但这不证明功能保留：冲突裁决或无文本冲突的调用链变化仍可能移除 TokenKey 行为。尤其不能把“fork 曾删除的路径”当作全部特性清单；删除理由及替代验证仍由 `docs/DEPRECATIONS.md` 拥有。

| 供应面 / 证据 | 上游可能带来的收益 | TokenKey 覆盖风险与验收要求 |
|---|---|---|
| 登录刷新：`7122efee9`；`auth_service.go`、`auth_service_tk_refresh_grace.go` 及其测试 | 原子消费旧 refresh token，避免并发重复兑换；缓存失败拒绝继续签发 | TokenKey 有 10 秒宽限期和会话绑定校验，重复刷新在窗口内允许成功。不能直接换成 upstream 的 GET+DEL 或因测试冲突删除宽限行为，否则多标签页/并发刷新可能重新报失效。原子轮转与宽限如何兼容涉及安全和体验取舍，须明确设计并覆盖并发、过期、禁用、改密、绑定变化和缓存失败；本报告不授权修改该策略。 |
| Codex WS→HTTP：`40d9c0964`；`openai_ws_http_bridge.go`、`openai_ws_forwarder_ingress.go` | 上游补齐桥接请求的指纹及缓存身份 | TokenKey 已在 ingress `parseClientPayload` 调用 `applyCodexAccountIdentityClientMetadataRaw`，桥接层明确禁止再次处理以免 session/thread UUID 重复哈希。不能直接叠加 upstream staging；验证首轮与后续轮身份稳定、跨账号隔离以及缓存键一致性。 |
| 调度槽位：`4ac713c4d`；`gateway_scheduling.go` | hydration 失败释放已获取的 Redis 并发槽位 | TokenKey 已在 `newSelectionResult` 的错误路径调用 `releaseProtocolSelectionOnPlanError`，随后经过 profit gate 和 `attachProtocolPlan`。这项收益不能重复计算；整段采用 upstream 实现反而可能丢失 Plan 裁决或错误释放。验证 hydrate/Plan 失败恰好释放一次，以及 slot 后的授权重验。 |
| 资格、授权、账号错误：`ebf016808`、`a1395eeaf`；`openai_account_runtime_block_fastpath.go`、middleware、gateway routes | 显式模型 cooldown、兼容路径账号错误策略更一致 | 必须保留 `docs/approved/candidate-eligibility-ssot.md` 的 Plan、实际账号平台、Direct/Universal 授权、billing origin/hold 重绑定及失败软降权 owner。特别检查 OpenAI OAuth 429 模型 cooldown 与 capacity-shed 不被改成账号级禁用，不能把上游局部策略覆盖成第二套调度真相。 |
| 计费与迁移：`fe093d76b`、`508bb9560`、`64e6c260c`；usage repo、Ent、pricing | 保留 OpenAI 30m/12h cache-write 明细，修正回退价与长上下文策略 | 要贯通 usage 解析 → SQL insert/query → 定价 → 结算 → 展示；保留 TokenKey 价格 owner 和幂等扣费/hold 生命周期。不能只更新 Ent 生成物就发布。迁移兼容与旧镜像回滚须另验，预演未执行迁移或账单对照。 |
| Codex 多轮/工具/图片：`0578fb160`、`142d22cc4`、`f2b0a32b1`、`cf1ba819b` | 防止后续轮误重放首帧、支持搜索激活重连、保留工具元数据和图片错误状态 | 无文本冲突仍须验证 TokenKey continuation 所有权、每轮授权/计费重验、已输出后禁止重放以及 QA terminal capture。更严格 fail-closed 可能增加断连，不能只用首轮成功作验收。 |
| Gemini/Antigravity：`36484727c`、`e2ce0b43d`、`0b535b5c0`、`d446c4a6b` | 避免思考摘要混入正文，保留工具媒体，适配 schema，限制异常 SSE/非流式读取 | 保留 Plan/converter、多模态 known-negative、模型级 quota/cooldown、流中断已产出计费和共享终止事件。读取上限可能拒绝此前可用的大图片响应，须测边界与错误归类；旧测试文件不恢复不等于可删除其仍有效的行为断言。 |
| 管理与接入 UI：`4a22a75b3`、`5965ac4e9`、`b8de5e170`、Codex export 改动 | 刷新时保留账号行和额度、保存按钮更易触达、密钥列表减少等待、导出配置更新 | 保留 AdminShell 持久布局、TokenKey 品牌/平台、模型菜单与 public quickstart 的共享 owner；settings 并非天然低风险。桌面/移动端实际保存与恢复、导出后接入、列表刷新均需 Playwright 真实 UI 验证。 |

现有保护入口包括 `scripts/sentinels/{gateway-tk,engine-facade,relay-invariants,newapi,kiro,grok,antigravity,frontend-tk,brand,trajectory,terminal,pricing-availability,perf-query-shape}.json` 及 preflight 对应检查器。真正 merge 时应核对实现入口、关键 call site 与行为测试是否同存；现有 registry/marker 通过不能替代上述运行验证。若适配引入新的承重挂点，应补 focused regression 和相应 sentinel，不能只确认文件仍在。

收益判断：后续正确集成有机会改善安全、长会话稳定性、流式内容、计费准确性和管理 UI；这些均为源码推导的潜在收益，不是本 PR 已实现或压测所得。主要负面风险是登录宽限退化、身份重复变换、资格/计费 owner 被旁路、流读取限制与多轮 fail-closed 带来的兼容性变化，以及迁移回滚成本。尚无最终解冲突 tree、全量行为测试或上线 canary，不能据此批准上游整体发布，也不能给出吞吐/错误率改善幅度。

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

上述列表仅枚举非 merge 提交；其余非 merge 提交的单提交预演至少有一个冲突路径。上游 merge 提交的完整集成须按前述方案裁决，不能按普通单父 cherry-pick 推断。

## 验证命令

```text
git fetch origin --tags
git fetch upstream --tags
bash scripts/upstream/check-drift.sh
node /Users/feng/.codex/skills/upstream-merge/scripts/audit.mjs --base 77f6f14977222171b5aba41ae99b8d078f01c42c --upstream e2c9812190dd71bb309c8ad020a1211da1e0d3b4 --head 77f6f14977222171b5aba41ae99b8d078f01c42c
git merge-tree --write-tree --messages --merge-base 25608d216bb7be04e4a646830cb065cd09b7d7e9 77f6f14977222171b5aba41ae99b8d078f01c42c e2c9812190dd71bb309c8ad020a1211da1e0d3b4
```

初始审计采集阶段未运行 Go/前端测试，没有产生代码合并。PR 原始 head `afbfcc1ba1e15aed9c9fa7438402796124a924bd` 已有 GitHub `preflight` 成功证据（run `42840560988`）；这只验证文档 PR，不验证潜在的上游集成。

2026-11-11 复审重跑固定 SHA 的 audit/merge-tree；冲突清单取自 index stage 路径，修正了把 modify/delete 提示末尾 `tree.` 误当文件名的问题。另对 `git rev-list --no-merges <fork base>..<upstream target>` 逐个使用其父提交作 `merge-tree --merge-base` 预演，复核上述无文本冲突列表。该模拟不进行依赖构建或业务验收。文档修订后仍须通过 preflight；任何实际 upstream merge 分支必须自行完成完整构建、回归与发布验证。
