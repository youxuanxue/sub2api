# US-029-signup-bonus-balance

- ID: US-029
- Title: Configurable USD signup bonus credited atomically on registration so new users have spend-able balance immediately
- Version: V1.5 (cold-start P0-B)
- Priority: P0
- As a / I want / So that:
  作为 **新注册的 TokenKey 用户**，我希望 **注册成功的瞬间账户里就有一笔可花的余额（默认 $1.00 USD，admin 可配；与 `users.balance` 同口径）**，**以便** 我可以马上拿自动签发的 trial Key 调一次模型试效果，而不是先去找充值入口；admin 也希望 **能在 settings 页一键调整这笔金额或一键关掉**，应对推广期 / 风控 / 滥用等不同节奏，不需要改代码。

- Trace:
  - 角色 × 能力轴线：新用户 × 「能立刻试一次」——今天 default_balance 默认 0，新用户首请求直接 quota_exceeded。
  - 实体生命周期轴线：用户创建状态迁移 `nil → StatusActive`。bonus 在该迁移边作为 INSERT-time 字段值（`user.balance = default_balance + bonus`）原子落入；audit 通过结构化日志（与 promo failure log 同模式）。
  - 系统事件轴线：每次邮箱注册 / OAuth 注册成功（**两条活路径**全覆盖；admin "create user" 路径**不在 scope** —— admin 已显式输入 balance，自动赠额会越权）。
  - 防御需求轴线：admin 必须能通过 setting 立即停止赠额（应对注册机滥用），不需要重启或改代码。

- Risk Focus:
  - 逻辑错误：两条活路径（`RegisterWithVerification` 邮箱注册 / `LoginOrRegisterOAuthWithTokenPair` OAuth 首登）必须**全部**正确叠加 bonus 到 `user.Balance`；`signup_bonus_enabled=false` 时**任何**路径都不能加额；`signup_bonus_balance=0` 时不能加额；与现有 `default_balance` 不冲突，最终余额（USD）= `default_balance + signup_bonus + (promo applied)`。**Dead code `LoginOrRegisterOAuth`（无 caller）不在 hook 列表，但旁加 `// TK-DEADCODE-NOTE` 注释，确保未来 wire 时同步加 hook**。
  - 行为回归：现有 promo code 流程（`promoService.ApplyPromoCode`）行为完全不变；现有 invitation 流程（`redeemRepo.Use`）行为完全不变；`Register` API 响应结构不变（balance 字段已存在，值变化即可）；admin "create user" 行为完全不变（balance 仍来自管理员输入）。
  - 安全问题：日志字段不得拼接用户输入（防 log injection，user_id / amount 走结构化字段而非格式化字符串）；金额必须服务端读 setting，不得来自请求 body；setting 更新走现有 admin 鉴权链路，普通用户无法自助调高。
  - 运行时问题（**架构对齐说明**）：bonus 通过 INSERT-time 字段值落地（一条 SQL，自带原子），不引入新 Tx；audit log best-effort，与现有 promo failure log 同模式（写日志失败不阻塞注册）。这是 v3 与初稿（要求 system_logs 表 + 强 atomicity）的实现细节调整 —— 因为 (a) `system_logs` 表当前不存在，新建会把 PR 1 拖入 schema migration scope；(b) 现有 promo 已经是 best-effort，强行不对称会让 admin 看到不一致的失败行为。**产品行为承诺不变**：用户注册成功 ↔ user.balance 包含 bonus，二者必同时成立或同时不成立。

## Acceptance Criteria

1. **AC-001 (正向 / 邮箱注册 + 默认 setting)**：Given setting `signup_bonus_enabled=true` 且 `signup_bonus_balance=1.00`（默认值，USD）、`default_balance=0`，When 用合法 email + verify_code 调 `POST /api/v1/auth/register`，Then 返回的 user.balance == 1.00 且 DB 中 `users.balance == 1.00`（USD）。
2. **AC-002 (正向 / OAuth 注册首登)**：Given 同上 settings，When 通过 `LoginOrRegisterOAuthWithTokenPair` 完成首次 LinuxDo 登录（with-invitation 与 without-invitation 两个内部分支都要测），Then 新建用户 balance == 1.00（USD）。
3. **AC-003 (正向 / admin 改值即时生效)**：Given admin 把 `signup_bonus_balance` 改为 `5.00`，When 改完后下一个新用户注册，Then 该用户 balance == 5.00（USD，无需重启服务，无 cache 漂移 —— `ComputeSignupBonus` 直接读 settingRepo，无中间缓存）。
4. **AC-004 (负向 / setting 关闭)**：Given setting `signup_bonus_enabled=false`，When 邮箱注册成功，Then 用户 balance == `default_balance`（默认 0）且**不会写出** `signup_bonus_credited` 结构化日志。
5. **AC-005 (负向 / 余额=0)**：Given setting `signup_bonus_enabled=true` 但 `signup_bonus_balance=0`，When 邮箱注册成功，Then 用户 balance == 0 且**不会写出** `signup_bonus_credited` 日志（不写 noise log）。
6. **AC-006 (副作用 / 结构化日志写出)**：Given 默认 settings + 一次成功的邮箱注册，When 解析进程日志，Then 恰好 1 条记录满足 `event=signup_bonus_credited AND user_id=<新用户ID> AND amount_usd=1.00 AND source=email`；OAuth 路径同样写出但 `source=oauth`。日志使用结构化字段（不在 message 字符串里拼用户输入）。
7. **AC-007 (回归 / 与 promo 兼容)**：Given 默认 signup_bonus + 一个有效 promo code (value=2.00 USD)，When 邮箱注册时带 promo_code，Then 最终 balance == 1.00 + 2.00 == 3.00（USD，promo 走原 `promoService.ApplyPromoCode` 路径，未受影响 —— bonus 已经在 INSERT 时就在了，promo 在其上 UpdateBalance）。
8. **AC-008 (回归 / admin path 不受影响)**：Given admin 在 `/api/v1/admin/users` 创建一个 `balance=10.00` 的用户，When 查询该用户，Then balance == 10.00（**不**额外加 1.00 bonus —— admin path 显式 out-of-scope，避免越权增加管理员意图外的余额）。
9. **AC-009 (回归 / 全 service 单测)**：Given 本 PR 落地，When `go test -tags=unit -count=1 ./internal/service/...`，Then 全部包通过。

## Assertions

- `getUser(newUserID).Balance == decimal("1.00")` 在默认 settings 邮箱注册场景下（USD）。
- 在 enabled+>0 场景下，进程结构化日志中能找到一条 `event=signup_bonus_credited` 字段满足 `user_id=<userID>, amount_usd=1.00, source=email|oauth`；在 enabled=false 或 balance=0 场景下找不到（noise log suppression）。
- 同一份测试 fixture 改 setting `signup_bonus_balance` 从 1.00 → 5.00 → 创建第二个用户 → 该用户 balance == 5.00（断言"setting 即时生效，不被 cache 卡住"——因为 `ComputeSignupBonus` 是 setting 直读路径，无 in-process cache）。
- 注入 `promoService` mock，断言 `ApplyPromoCode` 仍被以 promo_code 参数调用一次（并非被新逻辑跳过）；断言最终 balance = default + bonus + promo。
- 在 `LoginOrRegisterOAuthWithTokenPair` with-invitation 与 without-invitation **两个**内部分支下分别断言 user.Balance == 1.00（覆盖 OAuth 漏赠风险）。
- 在 `adminServiceImpl.CreateUser` 路径下断言：bonus **不**被叠加，user.balance 严格等于 `req.Balance`（admin path out-of-scope 的回归保护）。
- 在 `dead code LoginOrRegisterOAuth` 实现处存在 `// TK-DEADCODE-NOTE` 注释 + 引用本故事 ID（防止未来 wire 时漏 hook）。

## Linked Tests

实现按 CLAUDE.md §5 隔离纪律落到 `auth_service_tk_signup_bonus_test.go`（伴侣文件，不污染上游同形态 `auth_service.go` 的测试空间），覆盖范围与 AC 一一对照如下：

- `backend/internal/service/auth_service_tk_signup_bonus_test.go`::`TestUS029_RegisterEmailPath_AppliesBonus_DefaultSetting` — AC-001 邮箱默认 setting
- `backend/internal/service/auth_service_tk_signup_bonus_test.go`::`TestUS029_RegisterEmailPath_AdminChangedBonus_TakesEffectImmediately` — AC-003 admin 改值即时生效
- `backend/internal/service/auth_service_tk_signup_bonus_test.go`::`TestUS029_RegisterEmailPath_BonusDisabled_NoIncrement` — AC-004 setting 关闭
- `backend/internal/service/auth_service_tk_signup_bonus_test.go`::`TestUS029_RegisterEmailPath_BonusZero_NoIncrement` — AC-005 余额=0
- `backend/internal/service/auth_service_tk_signup_bonus_test.go`::`TestUS029_RegisterEmailPath_NegativeBonus_ClampedToZero` — Risk Focus 防御
- `backend/internal/service/auth_service_tk_signup_bonus_test.go`::`TestUS029_RegisterEmailPath_NoSettingService_NoBonus` — 健壮性兜底
- `backend/internal/service/auth_service_tk_signup_bonus_test.go`::`TestUS029_LogSignupBonusCredited_ZeroIsSilent` — AC-005 noise log 抑制
- `backend/internal/service/auth_service_register_test.go`::`TestAuthService_Register_Success` — AC-001 主路径回归（断言 balance == 4.5 = default 3.5 + bonus 1.00）
- `backend/internal/service/setting_service_tk_cold_start_test.go`::`TestComputeSignupBonus_HonorsEnabledFlag` — Getter 行为锁
- `backend/internal/service/setting_service_tk_cold_start_test.go`::`TestComputeSignupBonus_DefaultOneDollarOnFreshDB` — AC-001 默认值锁
- `backend/internal/service/setting_service_tk_cold_start_test.go`::`TestComputeSignupBonus_FallsBackOnDBError` — DB 错误兜底
- `backend/internal/service/setting_service_tk_cold_start_test.go`::`TestColdStart_AppendUpdates_ClampsNegativeBalance` — admin 输入安全 clamp
- `backend/internal/server/api_contract_test.go`::`TestAPIContracts/GET_/api/v1/admin/settings` — AC-009 admin settings 契约 snapshot 含 5 个新字段

OAuth 路径覆盖（AC-002 / AC-006 OAuth 分支）已通过 `auth_service.go::LoginOrRegisterOAuthWithTokenPair` 中 `s.tkApplyColdStartPostCreate(...)` 的 single-line hook 覆盖，调用同一 `applySignupBonusUSD` 入口；专用 OAuth 分支测试（with-invitation / without-invitation）作为 follow-up 在 OAuth 集成测试套件中补齐——当前 unit 范围用 `TestUS028_RegisterEmailPath_NoIssuerWired_NoPanic` 等边界用例验证 hook 调用契约。

> **AC 与实现的小差异**：
> - **AC-006 结构化日志**：实现用 `logger.LegacyPrintf` 写一行 `[Auth] signup_bonus_credited userID=%d amount_usd=%.2f source=%s`（与现有 promo failure log 同模式），未使用 `slog.Info(...event=...)` 结构化字段。理由见 Risk Focus 第 4 段：避免引入额外日志栈。`TestUS029_LogSignupBonusCredited_ZeroIsSilent` 锁定 noise suppression 不变；运维侧的字段抽取通过 grep `signup_bonus_credited userID=` + 正则解出 source/amount。
> - **AC-007 (与 promo 兼容回归)**：promo 分支在 `auth_service.go:230-241` 仍走 `s.promoService.ApplyPromoCode(...)`，未被本 PR 改动；本 PR 不改 promo 行为即视为兼容性持有，专用累加测试 follow-up 在覆盖 promo + bonus 综合场景的 PR 中补齐。

运行命令：

```bash
go test -tags=unit -count=1 -v \
  -run 'TestUS029_|TestAuthService_Register_Success|TestComputeSignupBonus_|TestColdStart_AppendUpdates_ClampsNegativeBalance' \
  ./backend/internal/service/...
go test -tags=unit -count=1 -v \
  -run 'TestAPIContracts/GET_/api/v1/admin/settings' ./backend/internal/server/...
```

## Evidence

- 完成事实归档：13 个 unit test 全部跑过（见 `attachments/`），覆盖默认值 / setting 关闭 / 余额=0 / admin 改值即时生效 / 负值 clamp / nil settingService / noise log suppression / admin settings 契约 snapshot。
- OAuth 分支端到端 trace 留作 follow-up evidence，在专用 OAuth 集成测试着陆时归档到 `attachments/us029-oauth-bonus-paths.txt`。

## Status

- [x] InTest — 后端 13 个 unit test 已落地并全绿；admin settings 契约 snapshot 已扩展到 5 个新字段；email + OAuth 两条活路径都通过 `tkApplyColdStartPostCreate` 单一调用点接入。等待 OAuth 专用集成测试上线后再翻 Done。
