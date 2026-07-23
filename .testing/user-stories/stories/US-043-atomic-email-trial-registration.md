# US-043-atomic-email-trial-registration

- ID: US-043
- Title: Effective Registration Offer 与原子 email-trial activation bundle
- Version: V1
- Priority: P0
- As a / I want / So that:
  作为 **首次访问 TokenKey 的用户**，我希望 **只有在完整试用可交付时才看到邮箱注册承诺，并在成功后直接获得赠额、可用 trial key 与 Quickstart**，**以便** 不遇到“注册成功但无法第一次调用”的半状态。
- Trace:
  - 设计锚点：`docs/approved/p0-conversion-trust.md` §6–§7。
  - Goal：`docs/task-breakdown-p0-conversion-trust-goals.md` P0-G3。
  - 被收敛旧行为：`docs/approved/user-cold-start.md` 的 email registration trial-key fail-open 部分（仅在本设计获批并实现后）。
- Risk Focus:
  - 逻辑错误：浏览器继续拼 flags，或设置关闭与注册提交没有可证明的线性顺序。
  - 行为回归：邀请、promo、OAuth、已有用户、登录与注册关闭路径被 email-trial 原子事务误接管。
  - 安全问题：幂等记录持久化密码、验证码、JWT 或 API key；验证码在事务失败前被消费。
  - 运行时：全局独占锁串行所有注册，或非 transaction-bound repository 造成 user/key/ledger 半提交。

## Acceptance Criteria

1. **AC-001（唯一 Offer）**：Given public settings，When 任一首页/价格/模型/登录/注册页面决定 trial CTA，Then 只读取 backend `registration_offer`；缺失、读取失败或 `state=unavailable` 时不显示注册、赠额或 key 承诺。
2. **AC-002（fail-closed predicate）**：Given backend mode、registration、bonus、trial key、默认 entitlement、SMTP/Turnstile 或 activation writer 任一未就绪/配置错误，When 计算 Offer，Then 返回 coarse public reason code 和无金额承诺的 unavailable 结果。
3. **AC-003（revision 顺序）**：Given 并发注册持有 guard-row `FOR SHARE`，When 管理员关闭 Offer，Then 已线性化事务可完成、关闭事务等待；关闭提交后旧 revision 的新请求返回 `offer_revision_changed`。正常注册之间不互斥。
4. **AC-004（原子 bundle）**：Given available email-trial Offer，When 注册成功，Then 同一 Ent transaction 已提交 user、signup balance journal、必需 entitlement/subscription、恰好一把 active universal trial key、适用 promo/invitation mutation 和 succeeded idempotency resource refs。
5. **AC-005（故障全回滚）**：Given AC-004 任一写点故障注入，When 注册，Then user/identity/balance/journal/entitlement/key/promo/idempotency 全部无残留，验证码仍可重试。
6. **AC-006（幂等恢复）**：Given 同一高熵 `Idempotency-Key` 丢失响应或并发重试，When fingerprint 相同且重放请求的密码校验 committed user 成功，Then即使验证码已消费也返回同一 user 并重新签 session，不重复 key/余额；密码错误或不同 fingerprint 返回错误且不签 session、不写入。
7. **AC-007（同邮箱并发）**：Given 不同幂等 key 同时注册同一规范化邮箱，When 执行，Then 只有一个用户和一把 trial key，另一请求返回 `email_already_registered`。
8. **AC-008（验证码生命周期）**：Given 合法验证码，When bundle 回滚，Then验证码未消费；When bundle 提交，Then使用原子 compare-delete 消费，删除失败也不能绕过邮箱唯一性创建第二用户。
9. **AC-009（首次成功）**：Given 成功响应，When frontend 跳转 `/quickstart` 并加载资源，Then trial key 与 required entitlement 已可见，非付费 canary 第一次调用返回 200。
10. **AC-010（上线门禁）**：Given实现与 staging 双态 E2E 完成，When发布功能代码，Then生产 `registration_enabled` 保持原值；只有独立 P0-LAUNCH 人工批准能开启。

## Assertions

- `RegistrationActivationService` 是唯一事务 owner；事务内无 Redis/SMTP/Turnstile/HTTP/token signing。
- trial key 和 idempotency 使用 `tx.Client()` narrow writer，不调用当前非 tx-aware generic repository。
- 幂等 fingerprint 不含秘密，现有 idempotency entity 只新增 nullable user/API-key resource ID，成功记录不序列化 auth response。
- `Idempotency-Key` 至少 128-bit、header 全链路脱敏且单独不能恢复登录；replay 必须再次通过 committed password hash 校验。
- `registration_session_unavailable` 可用相同幂等 key 恢复，不把已提交用户伪装为完整失败后再创建。

## Linked Tests

- `backend/internal/service/registration_offer_test.go`::`TestEffectiveRegistrationOfferFailsClosedAcrossDependencies` *(planned)*
- `backend/internal/service/registration_activation_integration_test.go`::`TestRegistrationActivationBundleRollsBackEveryWriteFailure` *(planned)*
- `backend/internal/service/registration_activation_integration_test.go`::`TestRegistrationOfferSharedLockOrdersCloseWithoutSerializingRegistrations` *(planned)*
- `backend/internal/service/registration_activation_integration_test.go`::`TestRegistrationIdempotencyRecoversWithoutDuplicateAssets` *(planned)*
- `frontend/e2e/email-trial-registration.spec.ts`::`available offer registers into a working Quickstart first call` *(planned)*
- `frontend/e2e/email-trial-registration.spec.ts`::`unavailable offer makes no trial promise` *(planned)*

运行命令：

```bash
cd backend && go test -tags=integration ./internal/service -run 'Registration(Offer|Activation)' -count=1
cd frontend && npm run test:e2e -- email-trial-registration.spec.ts
```

## Evidence

- 实现 PR 附 transaction failure matrix、并发 PostgreSQL 证据、staging 开关双态截图/trace 和生产 setting 未变证明。

## Status

- Ready — 等待设计批准与实现；生产邮箱注册不得在本 Story 中开启。
