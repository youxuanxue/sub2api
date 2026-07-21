# 运营账号导入

用 Admin API 批量创建 TokenKey 账号。支持平台：`anthropic` · `openai` · `gemini` · `antigravity` · `newapi` · `kiro` · `grok`。

**环境**：Python 3.10+，无需安装项目依赖。

## 1. 配置密钥

1. Admin 后台 → **系统设置 → 安全与认证 → 管理员 API Key** → 创建/复制（格式 `admin-...`，只显示一次）
2. 设置环境变量：

```bash
export TOKENKEY_BASE_URL="https://api.tokenkey.dev"   # 或 Stage0 / 自建域名
export TOKENKEY_ADMIN_API_KEY="admin-REPLACE_ME"
```

密钥等同管理员权限，勿提交 Git、勿外传。

## 2. 常用命令

```bash
cd ops/accounts

./import-accounts.sh validate examples/antigravity-oauth-export.json   # 校验 JSON
./import-accounts.sh import examples/antigravity-oauth-export.json --dry-run  # 预览，不写库
./import-accounts.sh import examples/antigravity-oauth-export.json --yes     # 确认导入
./import-accounts.sh import ./examples/ --yes                          # 批量导入目录
./import-accounts.sh list-channel-types                                # 查 newapi channel_type
./import-accounts.sh selftest                                          # 脚本自检
```

**建议流程**：`validate` → `--dry-run` → `--yes`。

## 3. JSON 怎么写

### 通用创建（任意平台）

```json
{
  "name": "my-account",
  "platform": "anthropic",
  "type": "apikey",
  "credentials": { "api_key": "REPLACE_ME" },
  "group_ids": [1],
  "concurrency": 3,
  "priority": 50
}
```

**newapi** 额外必填：`channel_type`（`list-channel-types` 可查）、`credentials.base_url`、`credentials.api_key`。

**kiro** 额外必填：`credentials.tos_acknowledged: true`，以及 `access_token` / `refresh_token` / `region` / `auth_method`。

### 批量

```json
{
  "group_ids": [1],
  "accounts": [
    { "name": "acc-1", "platform": "openai", "type": "apikey", "credentials": { "api_key": "REPLACE_ME" } }
  ]
}
```

### 快捷格式（脚本自动识别）

| 场景 | 怎么认 |
|------|--------|
| Antigravity OAuth 导出 | 含 `type: antigravity` 或 `access_token` + `project_id` |
| Codex session | 含 `accessToken` / `chatgpt_account_id` 等 |
| Grok SSO | `"import_profile": "grok_sso"` + `sso_tokens` |

也可显式指定 `"import_profile": "antigravity_oauth"` 等，详见 `examples/`。

### 重复导入

去重看 **OAuth 凭据**（refresh_token / email / access_token），不看显示名 `name`。**默认 `update_existing: true`**：同凭据更新已有账号；设为 `false` 则强制新建。同名不同凭据会正常新建，不拦截。

## 4. 示例文件

`examples/` 下各平台模板（凭据均为 `REPLACE_ME`）：`anthropic-apikey.json`、`openai-apikey.json`、`openai-codex-session.json`、`antigravity-oauth-export.json`、`newapi-deepseek-apikey.json`、`kiro-oauth-social.json`、`grok-sso-batch.json`、`batch-mixed.json` 等。

## 5. 注意

- 含真实 token / api_key 的 JSON 不要进 Git。
- 导入前务必 `--dry-run` 核对。
