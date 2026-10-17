# Node1 新域名登录配置交接

目标：迁移后的 API 和登录后台统一为 `https://api.galaxywaveai.com`。
用户确认由运营管理第三方配置。此清单只授权准备；**不开放新站业务、不影响 AWS prod，
正式切流仍须独立审批**。迁移状态与边界见 [准备记录](../approved/node1-prod-precutover.md)。

## 当前交接状态

运营已确认 Turnstile 允许域名及新 GitHub OAuth App 配置完成。新应用凭证已私有交接、
以 0600 暂存到 Node1；GitHub 凭证对照检查及 Node1 到 GitHub/Turnstile 的服务端验证
均通过。控制台配置的具体值和真实登录仍须浏览器验收，不能仅凭 Secret 有效视为通过。
用户要求暂缓该项，以下验收复选框保留待验收状态，不开放公网登录、不修改 AWS。

## 运营配置参考

### Cloudflare Turnstile

- 在 Cloudflare → Turnstile 找到当前 TokenKey 使用的 widget，核对 Site Key 与当前后台
  配置一致。Site Key 是公开标识；不要把 Secret Key 放在工单、截图或聊天中。
- 在该 widget 的 Hostname Management 中**新增** `api.galaxywaveai.com`，不带协议、
  端口或路径。保留全部既有域名及原 widget 模式，不轮换现网 Secret Key。
- 交回 widget 名称、是否已经允许该 hostname、修改时间；截图如有须遮蔽 Secret Key。
  若无法修改原 widget，可创建独立 widget，并通过本机私有文件交接新 Site Key/Secret Key，
  同时说明需要在 Node1 候选配置替换，不能覆盖 AWS 配置。
- 仅控制台保存成功不等于登录验收通过；后续在新域名受控验收中确认验证码挑战及服务端验证
  都通过。当前公开登录路径保持 503，不为验证而关闭验证码。

### GitHub 用户登录

基线已启用 GitHub 登录，现有回调是
`https://api.callmodel.io/api/v1/auth/oauth/github/callback`。
准备期**不修改现有 OAuth App 回调、不撤销或轮换它的密钥**，以免影响 AWS prod 登录。

推荐在正确的个人/组织账号下打开 Settings → Developer settings → OAuth Apps，
为新入口注册独立 OAuth App（不是供应账号授权，也不是 GitHub App）：

| 项目 | 填写值 |
|---|---|
| Application name | `TokenKey Node1`（运营可用明确的业务名称） |
| Homepage URL | `https://api.galaxywaveai.com` |
| Authorization callback URL | `https://api.galaxywaveai.com/api/v1/auth/oauth/github/callback` |
| Enable Device Flow | 保持关闭，本次不需要 |

- 交回应用归属、应用名称、Homepage/Callback 已配置的确认。
- Client ID 和 Client Secret 存入受保护的本机文件，权限 0600，只提供文件路径；不要贴到
  聊天、PR、仓库或截图。既有 GitHub 应用保持可用，供现网和回滚使用。
- 如果组织只能复用旧 OAuth App，请明确反馈这个限制。旧应用回调变更必须留到独立审批的
  切流窗口，不能提前执行，也不能把“已准备新回调”写成“已完成”。
- 新 App 使用同一个 GitHub 用户身份登录时，需要受控核验仍关联原 TokenKey 用户，
  不意外创建重复账号；检查登录返回、state 校验和后台权限。此项尚未实测。

## 由迁移执行人在批准窗口处理

以下均已列入待应用配置，**现在不改 AWS 或未脱敏的候选基线**：

| TokenKey 设置 | 新值 |
|---|---|
| `frontend_url` | `https://api.galaxywaveai.com` |
| `api_base_url` | `https://api.galaxywaveai.com` |
| `github_oauth_redirect_url` | `https://api.galaxywaveai.com/api/v1/auth/oauth/github/callback` |
| `github_oauth_frontend_redirect_url` | 保留相对路径 `/auth/oauth/callback` |
| `github_oauth_client_id` / `github_oauth_client_secret` | 运营交接的新应用凭证，仅用于 Node1 |

`SERVER_FRONTEND_URL` 已暂存为新域名，但数据库中的 `frontend_url` 优先，因此不能只改
环境变量。最终数据同步、逐用户用量和凭据对账先完成，再应用上述经审批的域名差异，避免
被同步覆盖。若新建了 Turnstile widget，同阶段替换 Node1 的对应密钥。

## 验收与交接

- [ ] Turnstile 允许新 hostname，既有域名未移除。
- [ ] 独立 GitHub OAuth App 已准备，现网 App 未改动。
- [ ] 凭证已私有交接，迁移执行人已核对候选配置。
- [ ] 新域名邮箱/密码登录与验证码通过；管理员仍有正确权限。
- [ ] GitHub 登录返回新域名，并关联已有用户；失败不会绕过认证。
- [ ] 页面显示的 API 地址、邀请/重置邮件链接均使用新域名；邮件验收需要专用收件人与
  单独发送授权，准备期不向真实用户发测试邮件。
- [ ] 客户端 Base URL/后台书签迁移清单准备好。新域名已有 DNS，正式切流不是单纯改 DNS。

旧 AWS 在切流前继续服务。切流时必须排空旧请求、停止旧写入和 OAuth 刷新，完成最终
数据同步后才开放新端，禁止两个节点同时写入。Redis 不迁移，新域名需要重新登录。
旧主机和旧登录应用至少保留在回滚期内；Node1 写入后回滚必须先回迁最新数据和轮换凭据。
