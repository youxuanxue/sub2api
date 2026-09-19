# 全平台账号批量复制与授权入口

## Background

管理员已有单个复制接口，但不支持 OAuth/setup-token，命名只追加 `(Copy)`。
需求是所有账号可复制、默认一个、可填写数量，并在账号行提供重新授权入口。

## Delta

- 复制对话框默认数量 1，允许整数 1–100；后端再次验证。
- 末尾 `-数字` 加一，其他名字追加 `-1`，连续生成 N 个名字；超长名称保留递增后缀并截断前缀至现有 100 字符限制。已有同名账号允许存在，沿用账号名称非唯一语义。
- 所有复制项与分组优先级在一次数据库事务中创建。重试保持同一幂等键；单个复制的原请求、响应和恢复标识保持兼容。
- 所有复制项暂停调度，保留平台、类型、凭据、代理、分组与配置，清除现有复制 owner 规定的运行时状态。OAuth 后台刷新候选已有 `schedulable = TRUE` 条件，因此暂停项不会抢用源账号的轮换令牌。
- Spark 影子复制为独立、暂停的普通 OAuth 账号，保留模型配置，不继承母账号关系或母账号凭据，需独立授权。
- OAuth/setup-token 行显示授权入口，复用现有平台会话。Kiro 使用已有凭据导入编辑器，Cursor 使用已有连接流程。Edge 账号继续在所属 Edge 的账号管理页执行凭据类操作。

## Implementation / Owners

| 行为 | 唯一 owner |
| --- | --- |
| 递增命名、复制配置、重试恢复 | `backend/internal/service/admin_account.go` |
| 批量事务、分组与调度 outbox | `backend/internal/repository/account_repo.go` |
| HTTP 数量校验、兼容返回与脱敏 | `backend/internal/handler/admin/account_handler.go` |
| 浏览器幂等键保存与恢复 | `frontend/src/api/admin/accounts.ts` |
| 数量输入、进行中保护、失败后重试 | `frontend/src/components/admin/account/DuplicateAccountModal.vue` |
| 菜单与账号状态列授权行为选择 | `frontend/src/utils/accountAuthorization.ts` |
| 状态列授权入口展示 | `frontend/src/components/account/AccountStatusIndicator.vue` |
| 授权 URL、回调与保存 | 既有 `ReAuthAccountModal.vue` 与对应 OAuth composable |

## Scenarios / Validation

- service 单测：跨平台 OAuth 批量复制、命名进位和长后缀、非法数量、同键重试、影子转换、源账号不变。
- handler 单测：空请求兼容、批量凭据脱敏、幂等重放、非法数量拒绝。
- PostgreSQL integration：第二条创建失败回滚第一条及 outbox；整批成功后可读回。
- Vue/API 单测：数量参数、失败重试键、菜单/对话框接线和授权路由。
- Playwright `account-duplicate.e2e.ts`：真实页面输入数量、失败重试、查看递增新账号并完成该账号的 OAuth 保存；外部授权请求使用本地 fixture，不消耗真实账号授权。
