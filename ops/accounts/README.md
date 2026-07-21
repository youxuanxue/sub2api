# TokenKey 运营账号导入工具

通过 Admin API 程序化创建/导入 TokenKey 账号，覆盖全部调度平台：

`anthropic` · `openai` · `gemini` · `antigravity` · `newapi` · `kiro` · `grok`

## 授权密钥（Admin API Key）

**是的**，推荐使用 Admin 后台生成的 **管理员 API Key**：

1. 浏览器登录 Admin：`https://api.tokenkey.dev/admin/settings`（或你的 Stage0 / 自建域名）
2. 打开 **系统设置 → 安全与认证 → 管理员 API Key**
3. 若尚未配置，点击 **创建密钥**；若已配置但需要明文，只能 **重新生成**（旧密钥立即失效）
4. 复制完整密钥（格式 `admin-<64位hex>`，仅显示一次）
5. 导出环境变量：

```bash
export TOKENKEY_BASE_URL="https://api.tokenkey.dev"
export TOKENKEY_ADMIN_API_KEY="admin-REPLACE_ME"
```

请求头用法（脚本已自动添加）：

```http
x-api-key: admin-<your-key>
Content-Type: application/json
```

说明：

- Admin JWT（浏览器登录后的 Bearer Token）也能访问 Admin API，但**运营脚本 / CI / 外部系统集成应使用 Admin API Key**（见 `docs/ADMIN_PAYMENT_INTEGRATION_API.md`）。
- Admin API Key 拥有完整管理员权限，请妥善保管，不要提交到 Git。

## 快速开始

```bash
cd ops/accounts

# 离线校验 JSON 格式
./import-accounts.sh validate examples/antigravity-oauth-export.json

# 预览将发送的 Admin API 请求（不写库）
./import-accounts.sh import examples/antigravity-oauth-export.json --dry-run

# 真实导入（必须 --yes）
./import-accounts.sh import examples/antigravity-oauth-export.json --yes

# 批量导入目录下所有 *.json
./import-accounts.sh import ./examples/ --yes

# 查看 newapi channel_type 列表（需 Admin API Key）
./import-accounts.sh list-channel-types

# 离线单元测试
./import-accounts.sh selftest
```

## JSON 格式

### 1. 标准创建（所有平台通用）

适用于 `POST /api/v1/admin/accounts`：

```json
{
  "name": "my-account",
  "platform": "anthropic",
  "type": "apikey",
  "credentials": {
    "api_key": "REPLACE_ME"
  },
  "group_ids": [1],
  "concurrency": 3,
  "priority": 50
}
```

`newapi` 额外必填：

- `channel_type`（> 0，见 `list-channel-types`）
- `credentials.base_url`
- `credentials.api_key`（或 Vertex SA JSON 等，取决于 channel）

`kiro` 额外必填：

- `credentials.tos_acknowledged`: `true`
- `credentials.access_token` / `refresh_token` / `region` / `auth_method`

### 2. 批量 bundle

```json
{
  "name": "batch-prefix",
  "group_ids": [1],
  "accounts": [
    { "name": "acc-1", "platform": "openai", "type": "apikey", "credentials": { "api_key": "REPLACE_ME" } },
    { "name": "acc-2", "platform": "newapi", "type": "apikey", "channel_type": 14, "credentials": { "api_key": "REPLACE_ME", "base_url": "https://upstream.example.com" } }
  ]
}
```

### 3. 平台快捷导入

| 场景 | import_profile / 自动识别 | Admin API |
|------|---------------------------|-----------|
| Antigravity OAuth 导出 JSON | `type: antigravity` 或含 `access_token`+`project_id` | `POST /admin/accounts/import/antigravity-oauth` |
| OpenAI Codex session JSON | `accessToken` / `chatgpt_account_id` 等 | `POST /admin/accounts/import/codex-session` |
| Grok SSO token 批量 | `import_profile: grok_sso` + `sso_tokens` | `POST /admin/grok/sso-to-oauth` |

显式指定：

```json
{
  "import_profile": "antigravity_oauth",
  "name": "ops-import",
  "group_ids": [1],
  "type": "antigravity",
  "access_token": "REPLACE_ME",
  "refresh_token": "REPLACE_ME",
  "project_id": "REPLACE_ME",
  "email": "user@example.com"
}
```

也可直接传入本地文件内容（脚本会把对象序列化为 `content` 字段）。

**生产环境兼容**：若服务端尚未部署 `POST /admin/accounts/import/antigravity-oauth`（返回 404），脚本会自动把 Antigravity OAuth 导出 JSON 转换为标准 `POST /admin/accounts` 创建请求（JSON 中必须包含 `project_id`）。

## 示例文件

`examples/` 目录包含各平台最小模板（凭据均为 `REPLACE_ME`）：

- `anthropic-apikey.json`
- `anthropic-oauth-cookie.json`（需自行填入 cookie 交换后的 credentials）
- `anthropic-bedrock.json`
- `openai-apikey.json`
- `openai-codex-session.json`
- `gemini-oauth.json`
- `gemini-service-account.json`
- `antigravity-oauth-export.json`
- `antigravity-upstream-apikey.json`
- `newapi-deepseek-apikey.json`（示例 channel_type=14，请按实际 upstream 调整）
- `newapi-vertex-service-account.json`（channel_type=41）
- `kiro-oauth-social.json`
- `grok-oauth-refresh.json`
- `grok-sso-batch.json`
- `batch-mixed.json`

## 调用的 Admin API 端点

| 路由检测 | HTTP |
|----------|------|
| `create_account` | `POST /api/v1/admin/accounts` |
| `batch_bundle`（纯 create 行） | `POST /api/v1/admin/accounts/batch` |
| `antigravity_oauth` | `POST /api/v1/admin/accounts/import/antigravity-oauth` |
| `codex_session` | `POST /api/v1/admin/accounts/import/codex-session` |
| `grok_sso` | `POST /api/v1/admin/grok/sso-to-oauth` |

## 安全提示

- 不要把含真实 `access_token` / `refresh_token` / `api_key` 的 JSON 提交到 Git。
- 导入前用 `validate` / `--dry-run` 确认路由与字段。
- 生产环境建议为每次导入使用独立 Idempotency-Key（脚本已按 payload 哈希自动生成）。
