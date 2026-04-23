# US-028-signup-bonus-balance

- ID: US-028
- Title: Configurable USD signup bonus credited atomically on registration so new users have spend-able balance immediately
- Version: V1.5 (cold-start P0-B)
- Priority: P0
- As a / I want / So that:
  作为 **新注册的 TokenKey 用户**，我希望 **注册成功的瞬间账户里就有一笔可花的余额（默认 $1.00 USD，admin 可配；与 `users.balance` 同口径）**，**以便** 我可以马上拿自动签发的 trial Key 调一次模型试效果，而不是先去找充值入口；admin 也希望 **能在 settings 页一键调整这笔金额或一键关掉**，应对推广期 / 风控 / 滥用等不同节奏，不需要改代码。

- Trace:
  - 角色 × 能力轴线：新用户 × 「能立刻试一次」——今天 default_balance 默认 0，新用户首请求直接 quota_exceeded。
  - 实体生命周期轴线：用户创建状态迁移 `nil → StatusActive`。本故事在该迁移边补一笔 balance + system_log 副作用，不得只改其中一处。
  - 系统事件轴线：每次邮箱注册 / OAuth 注册成功（3 条调用路径都要覆盖，否则有路径漏赠）。
  - 防御需求轴线：admin 必须能通过 setting 立即停止赠额（应对注册机滥用），不需要重启或改代码。

- Risk Focus:
  - 逻辑错误：3 条注册路径（`RegisterWithVerification` / `LoginOrRegisterOAuth` / `LoginOrRegisterOAuthWithTokenPair`）必须**全部**写入赠额 + 全部写 system_log；`signup_bonus_enabled=false` 时**任何**路径都不能加额；`signup_bonus_balance=0` 时不能加额也不能写 log；与现有 `default_balance` 不冲突，最终余额（USD）= `default_balance + signup_bonus + (promo applied)`。
  - 行为回归：现有 promo code 流程（`promoService.ApplyPromoCode`）行为完全不变；现有 invitation 流程（`redeemRepo.Use`）行为完全不变；`Register` API 响应结构不变（balance 字段已存在，值变化即可）。
  - 安全问题：notes 字段不得拼接用户输入（防 log injection）；金额必须服务端读 setting，不得来自请求 body；setting 更新走现有 admin 鉴权链路，普通用户无法自助调高。
  - 运行时问题：在事务内同时写 user + system_log；任一失败回滚（不能出现"用户创建成功但赠额没加"或"赠额加了但用户事务回滚"的中间态）。

## Acceptance Criteria

1. **AC-001 (正向 / 邮箱注册 + 默认 setting)**：Given setting `signup_bonus_enabled=true` 且 `signup_bonus_balance=1.00`（默认值，USD）、`default_balance=0`，When 用合法 email + verify_code 调 `POST /api/v1/auth/register`，Then 返回的 user.balance == 1.00 且 DB 中 `users.balance == 1.00`（USD）。
2. **AC-002 (正向 / OAuth 注册首登)**：Given 同上 settings，When 通过 `LoginOrRegisterOAuthWithTokenPair` 完成首次 LinuxDo 登录，Then 新建用户 balance == 1.00（USD）。
3. **AC-003 (正向 / admin 改值即时生效)**：Given admin 把 `signup_bonus_balance` 改为 `5.00`，When 改完后下一个新用户注册，Then 该用户 balance == 5.00（USD，无需重启服务，无 cache 漂移）。
4. **AC-004 (负向 / setting 关闭)**：Given setting `signup_bonus_enabled=false`，When 邮箱注册成功，Then 用户 balance == `default_balance`（默认 0）且 `system_logs` 表**没有** `type='signup_bonus'` 的新记录。
5. **AC-005 (负向 / 余额=0)**：Given setting `signup_bonus_enabled=true` 但 `signup_bonus_balance=0`，When 邮箱注册成功，Then 用户 balance == 0 且**没有** `system_logs.signup_bonus` 记录（不写 noise log）。
6. **AC-006 (副作用 / system_log 写入)**：Given 默认 settings + 一次成功的邮箱注册，When 查询 `system_logs WHERE user_id=<新用户> AND type='signup_bonus'`，Then 恰好 1 条，amount=1.00，notes 为 `"signup bonus = $1.00 USD"`（精确字符串，含美元符号与 USD 单位）。
7. **AC-007 (回归 / 与 promo 兼容)**：Given 默认 signup_bonus + 一个有效 promo code (value=2.00 USD)，When 邮箱注册时带 promo_code，Then 最终 balance == 1.00 + 2.00 == 3.00（USD，promo 走原 `promoService.ApplyPromoCode` 路径，未受影响）。
8. **AC-008 (回归 / 全 service 单测)**：Given 本 PR 落地，When `go test -tags=unit -count=1 ./internal/service/...`，Then 全部包通过。

## Assertions

- `getUser(newUserID).Balance == decimal("1.00")` 在默认 settings 邮箱注册场景下（USD）。
- `len(systemLogRepo.ListByUserAndType(ctx, userID, "signup_bonus"))` 在 enabled+>0 时 == 1，在 enabled=false 或 balance=0 时 == 0。
- 同一份 system_log 记录的 `notes` 字段精确匹配 `"signup bonus = $1.00 USD"`（USD 字面值是 setting key 命名的反向兜底——若有人把 currency 默默改成 CNY，此断言立刻失败）。
- 同一份测试 fixture 改 setting `signup_bonus_balance` 从 1.00 → 5.00 → 创建第二个用户 → 该用户 balance == 5.00（断言"setting 即时生效，不被 cache 卡住"）。
- 注入 `promoService` mock，断言 `ApplyPromoCode` 仍被以 promo_code 参数调用一次（并非被新逻辑跳过）。
- 在 `LoginOrRegisterOAuthWithTokenPair` 路径下断言 user.Balance == 1.00（覆盖 OAuth 漏赠风险）。
- `signup_bonus` 写入路径处于事务内：注入一个 `userRepo.Create` 成功但 `systemLogRepo.Create` 失败的 fixture，断言整个注册返回错误且 DB 中**没有**新用户（事务原子性）。

## Linked Tests

- `backend/internal/service/us028_signup_bonus_test.go`::`TestUS028_EmailRegisterAddsBonus`
- `backend/internal/service/us028_signup_bonus_test.go`::`TestUS028_OAuthRegisterAddsBonus`
- `backend/internal/service/us028_signup_bonus_test.go`::`TestUS028_AdminUpdateTakesEffectImmediately`
- `backend/internal/service/us028_signup_bonus_test.go`::`TestUS028_DisabledSettingNoBonus`
- `backend/internal/service/us028_signup_bonus_test.go`::`TestUS028_ZeroBalanceNoBonusNoLog`
- `backend/internal/service/us028_signup_bonus_test.go`::`TestUS028_SystemLogWritten`
- `backend/internal/service/us028_signup_bonus_test.go`::`TestUS028_CompatibleWithPromo`
- `backend/internal/service/us028_signup_bonus_test.go`::`TestUS028_TransactionAtomicity`

运行命令：

```bash
go test -tags=unit -count=1 -v -run 'TestUS028_' ./backend/internal/service/...
```

## Evidence

- 待 PR 1 实现完成后归档到 `.testing/user-stories/attachments/us028-signup-bonus-paths.txt`（含 3 条注册路径的执行 trace 截取）。

## Status

- [ ] Draft — 设计已定，等待审批；审批通过后进入 InTest。
