# US-030-auto-first-api-key

- ID: US-030
- Title: Auto-issue a "trial" API key on registration so new users can curl the gateway in their first session
- Version: V1.5 (cold-start P0-C)
- Priority: P0
- As a / I want / So that:
  作为 **新注册的 TokenKey 用户**，我希望 **注册成功后 dashboard 上立刻就有一把名为 `trial` 的 Key 可复制 + 一段现成的 `curl` 例子**，**以便** 我能在新手页面 30 秒内打通"复制 → 粘贴 → 200 OK"链路；不再像 L 站 `t/topic/1545819` 用户描述的那样「找了半天 key 在哪儿、模型该怎么写」就放弃；admin 也希望 **能一键关掉这个自动行为，或把 Key 默认名换成自己想要的字面值**（部分场景需要人工审核才发 Key，或想叫 `default`/`free`/品牌名）。

- Trace:
  - 角色 × 能力轴线：新用户 × 「能 curl 一次」——今天首页 stats 全 0、API Keys 列表为空、没有任何 quick start 引导。
  - 实体生命周期轴线：用户创建后 → API Key 创建（`null → StatusActive`），本故事补齐这条隐含但缺失的迁移。
  - 系统事件轴线：每次邮箱注册 / OAuth 注册成功（与 US-029 同 3 路径）。
  - 防御需求轴线：默认 Key 不应绕过任何 quota / rate-limit；admin 可关。

- Risk Focus:
  - 逻辑错误：3 条注册路径必须全部尝试创建 trial key；`auto_generate_default_token=false` 时全部不创建；如果用户已存在（OAuth 老用户登录），**不**应再创建（要判断 `apiKeyRepo.CountByUserID(userID) == 0`）；`auto_generate_default_token_name` 必须被读到（不能硬编码 "trial"）。
  - 行为回归：API Key 创建链路必须复用现有 `apiKeyService.Create`，不绕过任何鉴权 / 限流 / 验证（GroupID 解析、quota 默认值、rate limit 字段等都按现有规则）；`Register` API 响应结构不变（不强制返回 key 明文，前端通过 `/api/v1/users/me/keys?first=true` 获取）。
  - 安全问题：自动建 Key 失败时**不得**阻塞注册成功（fail-open），且失败原因必须打日志便于运维排查；脱敏字段（key 明文）只在创建后第一次返回，不进任何缓存。
  - 运行时问题：自动建 Key 与注册事务**解耦**（建 Key 失败不回滚注册），但保留可观测性。

## Acceptance Criteria

1. **AC-001 (正向 / 邮箱注册自动建 Key)**：Given setting `auto_generate_default_token=true`（默认）、`auto_generate_default_token_name="trial"`（默认），When 邮箱注册成功，Then `api_keys` 表中存在 1 条 `user_id=<新用户>, name="trial", status="active"` 的记录。
2. **AC-002 (正向 / OAuth 首登自动建 Key)**：Given 同上 settings，When `LoginOrRegisterOAuthWithTokenPair` 创建新用户，Then 同 AC-001（OAuth 路径不漏，name 仍是 `"trial"`）。
3. **AC-003 (负向 / setting 关闭)**：Given setting `auto_generate_default_token=false`，When 邮箱注册成功，Then `api_keys` 表中**没有**该用户的任何 Key 记录。
4. **AC-004 (负向 / 老用户 OAuth 重登不重复建)**：Given 一个已存在用户（已有 1 把手动创建的 Key），When 通过 OAuth 重新登录该用户，Then `api_keys` 表中该用户仍只有原 1 把 Key（不会因 OAuth 重登重复 auto-create）。
5. **AC-005 (鲁棒 / 建 Key 失败不阻塞注册)**：Given 注入 `apiKeyService.Create` 返回 error 的 mock，When 邮箱注册流程跑完，Then 注册 API 返回 200 + 用户在 DB（用户必须创建成功），但 `api_keys` 表无该用户记录，且 server log 有一行 `[Auth] Failed to auto-create trial key for user X` 错误日志。
6. **AC-006 (副作用 / 自动 Key 走完整鉴权链)**：Given 自动创建的 trial Key，When 持该 Key 调 `GET /v1/models`，Then 200 且模型列表与该用户所属 group 的可用模型一致（不绕过 quota / group binding 等任何中间件）。
7. **AC-007 (前端 / Quick Start 卡片显示)**：Given 新注册用户的 dashboard 首次加载（`stats.total_api_keys==1` 且 `stats.first_request_at==null`），When 渲染 `DashboardView`，Then 顶部出现 `UserDashboardQuickStart` 卡片含脱敏 Key + 复制按钮 + curl 例子 + 文案 `"试用额度 ${bonus}，用完不会自动扣费"`；当用户发出第一笔请求后下次访问 dashboard，该卡片**消失**。
8. **AC-008 (admin 自定义名)**：Given admin 把 `auto_generate_default_token_name` 改为 `"welcome"`，When 改完后下一个新用户注册，Then 该用户的自动 Key `name == "welcome"`（断言 setting 被读到，不是硬编码 `"trial"`）。
9. **AC-009 (回归 / api_key_service 单测)**：Given 本 PR 落地，When 执行现有 `api_key_service_*_test.go`，Then 全部通过（不动现有断言）。

## Assertions

- `apiKeyRepo.CountByUserID(ctx, newUserID) == 1` 在默认 settings 注册场景。
- `apiKeyRepo.ListByUserID(ctx, newUserID)[0].Name == "trial"` 在默认 setting name。
- 改 setting `auto_generate_default_token_name` → `"welcome"` 后再创建一个新用户，断言 `apiKeyRepo.ListByUserID(ctx, secondUserID)[0].Name == "welcome"`（覆盖"setting 名字真的被读"，避免硬编码回归）。
- `apiKeyRepo.CountByUserID(ctx, oauthLoginUserID) == 1`（重登场景下不重复建）。
- 注入 `apiKeyService.Create` 返回 `errors.New("forced failure")`，断言 `RegisterWithVerification` 返回 nil error + user 已在 DB（fail-open 不阻塞）。
- 自动 trial key 调 `/v1/chat/completions` 在 quota 用尽时返回 402 / 429（与人工创建的 Key 行为一致，没有特殊免检）。
- 前端组件单测：`mountUserDashboardQuickStart({ totalKeys: 1, firstRequestAt: null })` → 包含 `data-testid="quick-start-card"`；`mountUserDashboardQuickStart({ totalKeys: 1, firstRequestAt: '2026-04-22T...' })` → 不包含该 testid。

## Linked Tests

- `backend/internal/service/us030_auto_first_key_test.go`::`TestUS030_EmailRegisterCreatesTrialKey`
- `backend/internal/service/us030_auto_first_key_test.go`::`TestUS030_OAuthRegisterCreatesTrialKey`
- `backend/internal/service/us030_auto_first_key_test.go`::`TestUS030_DisabledSettingNoKey`
- `backend/internal/service/us030_auto_first_key_test.go`::`TestUS030_OAuthReloginDoesNotDuplicate`
- `backend/internal/service/us030_auto_first_key_test.go`::`TestUS030_KeyCreationFailureDoesNotBlockSignup`
- `backend/internal/service/us030_auto_first_key_test.go`::`TestUS030_AutoKeyHonorsQuotaMiddleware`
- `backend/internal/service/us030_auto_first_key_test.go`::`TestUS030_AdminCustomKeyName`
- `frontend/src/__tests__/components/UserDashboardQuickStart.spec.ts`::`shows when first request null and total keys = 1`
- `frontend/src/__tests__/components/UserDashboardQuickStart.spec.ts`::`hides after first request`

运行命令：

```bash
go test -tags=unit -count=1 -v -run 'TestUS030_' ./backend/internal/service/...
pnpm --filter frontend vitest run __tests__/components/UserDashboardQuickStart
```

## Evidence

- 待 PR 1 实现完成后归档到 `.testing/user-stories/attachments/us030-curl-trace.txt`（含一次"刚注册 → 拷贝 curl → 200 OK"完整 HTTP 抓取）。

## Status

- [ ] Draft — 设计已定，等待审批；审批通过后进入 InTest。
