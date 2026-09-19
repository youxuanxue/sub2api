# Antigravity 账号恢复链接

## Background

Google 403 的验证链接已由网关写进账号临时冷却原因，旧版本或写冷却失败时可能写进错误信息。运营需要从该账号直接复制链接，并区分验证挑战与重新 OAuth 授权。

## Delta

- 账号状态展示最近一次保存的 `validation_url:`，无需再次查询上游 usage。清除相应原因后，链接随之消失；冷却到期本身不等于 Google 验证通过。
- 仅将 HTTPS 的 `accounts.google.com`、`support.google.com` URL 转为可操作链接，拒绝带用户信息、非标准端口和伪造域名。
- Antigravity OAuth 账号状态新增授权链接入口。点击打开该账号的现有重新授权弹窗并自动生成链接，复制到使用所属 Edge 出口的指纹浏览器，回填完整 localhost 回调地址后完成授权。
- 链接与 PKCE session 保持同一生命周期，不保存到账号 extra 或浏览器存储。服务器返回其实际过期时间；前端过期时清空链接和 session，关闭或切换账号后忽略迟到的生成响应。重启服务后仍须重新生成。
- 授权回填使用已有 `apply-oauth-credentials` 接口，统一清除错误和失效 token 缓存，避免旧 `update` + `clear-error` 流程继续使用旧 token。
- Edge 概览复用验证链接展示；OAuth 继续从现有“管理 Edge”入口进入该 Edge 管理页进行，不从生产节点替 Edge 交换凭据。

## Owners

| 行为 | 唯一 owner | 消费入口 |
| --- | --- | --- |
| 从账号错误提取链接、校验 Google 目标 | `frontend/src/utils/antigravityRecovery.ts` | 共享状态组件、验证链接组件 |
| 验证链接打开与复制 | `frontend/src/components/account/GoogleVerificationLink.vue` | `AccountStatusIndicator`、`AntigravityUsageCell`（普通与 Edge 账号页面复用） |
| OAuth 生成、过期、关闭后响应隔离 | `frontend/src/composables/useAntigravityOAuth.ts` | 创建与重新授权弹窗 |
| state / PKCE / session 有效期 | `backend/internal/service/antigravity_oauth_service.go` + `internal/pkg/antigravity/oauth.go` | 现有 OAuth API |

展示与行为 owner 注册在 `scripts/sentinels/frontend-tk.json`。

## Scenarios / Validation

- 正向：从账号状态复制原始 Google 验证链接；OAuth 链接生成使用账号代理；回填 code/state 后只更新该账号。
- 负向：不安全链接不展示；非 Antigravity OAuth 不展示授权入口；生成失败可重试；已过期或已关闭弹窗的生成响应不得恢复链接；后端拒绝错误 state 与过期 session。
- 回归：旧 Edge 未返回 `expires_at` 时沿用原有 30 分钟 TTL；原有重新授权菜单与 usage 单元格保留。
- 自动测试：`AntigravityRecoveryLinks.spec.ts`、`useAntigravityOAuth.spec.ts`、`TestAntigravityAuthorizationLinkMatchesSession`。
- Playwright：`frontend/e2e/antigravity-recovery-links.e2e.ts` 在桌面和移动视口驱动真实账号页面，通过模拟 API 完成生成失败重试、复制、回调解析和账号更新；不访问 Google、不使用线上账号。
