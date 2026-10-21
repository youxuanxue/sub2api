# Antigravity 账号恢复链接

## Background

Google 403 的验证链接已由网关写进账号临时冷却原因，旧版本或写冷却失败时可能写进错误信息。运营需要从该账号直接复制链接，并区分验证挑战与重新 OAuth 授权。

## Delta

- 账号状态展示最近一次保存的验证链接，无需再次查询上游 usage。账号测试遇到 Google validation 403 时复用网关的挑战处理；底层能力探测保持只读。共享处理器在冷却去重前更新 `extra.antigravity_validation_url`，状态组件优先展示该最新链接，兼容旧账号仅有原因文本的情况。恢复账号时通过共享 `ClearTempUnschedulable` 同时清除最新 URL 与原因，链接随之消失；冷却到期本身不等于 Google 验证通过。
- 仅将 HTTPS 的 `accounts.google.com`、`support.google.com` URL 转为可操作链接，拒绝带用户信息、非标准端口和伪造域名。
- Antigravity OAuth 账号状态新增授权链接入口。点击打开该账号的现有重新授权弹窗并自动生成链接，复制到使用账号代理出口的指纹浏览器，回填完整 localhost 回调地址后完成授权。
- OAuth 授权链接与 PKCE session 保持同一生命周期，不保存到账号 extra 或浏览器存储。服务器返回其实际过期时间；前端过期时清空链接和 session，同账号资料刷新保留正在进行的授权，关闭或切换账号后忽略迟到的生成响应。重启服务后仍须重新生成。
- 授权回填使用已有 `apply-oauth-credentials` 接口，统一清除错误和失效 token 缓存，避免旧 `update` + `clear-error` 流程继续使用旧 token。
- 共享授权组件在会话过期或被新会话替换时清空回调 code/state，避免重新生成链接后只回填 code 却误用旧 state。

## Owners

| 行为 | 唯一 owner | 消费入口 |
| --- | --- | --- |
| 最新验证 URL 记录与挑战处理 | `RateLimitService.handleAntigravity403` | 网关、管理员账号测试 |
| 验证链接恢复清理 | `accountRepository.ClearTempUnschedulable` | 重新授权、手工恢复、成功测试 |
| 从账号错误提取链接、校验 Google 目标 | `frontend/src/utils/antigravityRecovery.ts` | 共享状态组件、验证链接组件 |
| 验证链接打开与复制 | `frontend/src/components/account/GoogleVerificationLink.vue` | `AccountStatusIndicator`、`AntigravityUsageCell` |
| OAuth 生成、过期、关闭后响应隔离 | `frontend/src/composables/useAntigravityOAuth.ts` | 创建与重新授权弹窗 |
| state / PKCE / session 有效期 | `backend/internal/service/antigravity_oauth_service.go` + `backend/internal/pkg/antigravity/oauth.go` | 现有 OAuth API |

展示与行为 owner 注册在 `scripts/sentinels/frontend-tk.json`。

## Scenarios / Validation

- 正向：OAuth 完成后测试失败即刷新账号行并展示验证链接；同一冷却期间重复测试更新 URL 而不重复升级冷却；成功恢复后链接消失；从账号状态复制原始 Google 验证链接；OAuth 链接生成使用账号代理；回填 code/state 后只更新该账号。
- 负向：不安全链接不展示；非 Antigravity OAuth 不展示授权入口；生成失败可重试；已过期或已关闭弹窗的生成响应不得恢复链接；后端拒绝错误 state 与过期 session。
- 回归：API 未返回 `expires_at` 时沿用原有 30 分钟 TTL；原有重新授权菜单与 usage 单元格保留。
- 自动测试：`AntigravityRecoveryLinks.spec.ts`、`useAntigravityOAuth.spec.ts`、`TestAntigravityAuthorizationLinkMatchesSession`、`ReAuthAccountModal.antigravity.spec.ts`。
- Playwright：`frontend/e2e/antigravity-recovery-links.e2e.ts` 在桌面和移动视口驱动真实账号页面，通过模拟 API 完成生成失败重试、复制、回调解析和账号更新；不访问 Google、不使用线上账号。
