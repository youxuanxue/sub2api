# 运营账号导入

用 Admin API 批量创建 TokenKey 账号。支持平台：`anthropic` · `openai` · `gemini` · `antigravity` · `newapi` · `kiro` · `grok`。

**环境**：Python 3.10+，无需安装项目依赖。

---

## 先搞懂：TokenKey 上 OAuth 账号通常长什么样

线上大多数 OAuth 容量不是「prod 上直接挂真 OAuth」，而是 **两段式**：

```text
你的客户端（Claude Code / Codex / …）
        │
        ▼
   prod（api.tokenkey.dev）
   账号名如 cc-us6、ag-us6
   类型：API Key 镜像 stub
   base_url → https://api-us6.tokenkey.dev
   api_key  → 该 edge 上签发的 tk_...
        │
        ▼
   edge（api-us6.tokenkey.dev）
   真 OAuth 账号（refresh_token 在这里）
        │
        ▼
   上游（Anthropic / OpenAI / xAI / …）
```

**一句话**：prod 上的 `cc-us6` 不是真 OAuth，只是把流量转发到 edge；edge 上才是真 OAuth。

`gemini` 不走这套 edge 中继，只在单端直接导入。

---

## 三种导入方式，对号入座

| 你要干什么 | 用哪种 JSON | 需要几套 Admin Key |
|-----------|------------|-------------------|
| 只在 prod 或只在 edge 建一个账号（API Key、Bedrock、newapi 等） | 普通 `create_account` 或快捷格式 | **一套**（`TOKENKEY_BASE_URL`） |
| **标准 OAuth 上线**：edge 真 OAuth + prod 中继 stub | `import_profile: edge_oauth_relay` | **两套**（prod + edge） |
| 批量混导 | `batch_bundle` 包多个账号 | 看 bundle 里每项 |

下面重点讲最常用的 **`edge_oauth_relay`**。

---

## Edge OAuth + Prod 中继：完整流程

### 脚本一次会做什么

用 `import_profile: edge_oauth_relay` 时，脚本按顺序执行：

```text
① 在 edge 导入/更新真 OAuth（edge_oauth 块）
        │
        ▼
② 查 prod：是否已有「同 edge + 同 pool」的中继 stub？
        │
   ┌────┴────┐
   已有      没有
   │          │
   跳过 prod   ③ 在 edge 签发 tk_ API Key（可自动）
   │          │
   │          ④ 在 prod 新建 mirror stub（prod_relay 块）
   └────┬────┘
        ▼
      完成
```

**pool_platform** 决定 prod 上找哪条中继：`anthropic` · `openai` · `antigravity` · `grok` · `kiro`（与 edge 调度池一致）。  
例如 prod 上已有 `ag-us6`（base_url 指向 `api-us6`），再导同 edge 的 antigravity OAuth 时，prod 步骤会自动跳过。

### 四种常见情形

#### 情形 A：全新上线（prod 还没有 cc-us6 / ag-us6 这类 stub）

- JSON 里写好 `edge_id`、`pool_platform`、`edge_oauth`（真 OAuth 凭据）
- `prod_relay` 里写 stub 名字、分组，以及 **谁去 edge 领 key**：

```json
"prod_relay": {
  "name": "cc-us6",
  "group_ids": [1],
  "edge_api_key_user_id": 1,
  "edge_api_key_group_id": 1
}
```

脚本会：
1. edge 导入 OAuth  
2. 在 edge 给 `user_id=1` 签发（或复用同名）`tk_...`  
3. prod 创建 stub，`api_key` 填这个 `tk_...`，`base_url` 填 `https://api-us6.tokenkey.dev`

#### 情形 B：prod 中继已经有了，只是换/更新 edge 上的 OAuth

- prod 上 `cc-us6` 等 stub **已存在** → 脚本 **只动 edge**，prod 跳过（默认 `skip_prod_relay_if_exists: true`）
- 典型场景：OAuth token 刷新、换号、补账号，不想动 prod 分组和 stub

#### 情形 C：你手里已经有 edge 的 tk_ key，不想自动签发

- 直接在 JSON 里写死：

```json
"prod_relay": {
  "edge_api_key": "tk_你已经在 edge 后台签好的 key"
}
```

- 适合 key 是人工在 Admin UI 签的、或要沿用旧 key 的情况

#### 情形 D：只在 edge 或只在 prod 建账号（不走中继）

- **不要**用 `edge_oauth_relay`
- 用普通 JSON，只配 **一套** `TOKENKEY_BASE_URL` + `TOKENKEY_ADMIN_API_KEY`
- 示例：`examples/anthropic-apikey.json`、`examples/gemini-oauth.json` 等

---

## 操作步骤（每次导入都建议这样走）

```bash
cd ops/accounts

# 0. 配密钥（见下一节）

# 1. 检查 JSON 格式
./import-accounts.sh validate examples/edge-oauth-relay-anthropic-us6.json

# 2. 预演：看会创建/更新/跳过什么，不写库
./import-accounts.sh import examples/edge-oauth-relay-anthropic-us6.json --dry-run

# 3. 确认无误后再真写
./import-accounts.sh import examples/edge-oauth-relay-anthropic-us6.json --yes
```

辅助命令：

```bash
./import-accounts.sh list-edges          # 当前可部署的 edge 节点
./import-accounts.sh list-channel-types   # newapi channel_type 对照
./import-accounts.sh selftest             # 脚本自测
```

---

## 配置密钥

### 单端导入（情形 D）

```bash
export TOKENKEY_BASE_URL="https://api.tokenkey.dev"
export TOKENKEY_ADMIN_API_KEY="admin-REPLACE_ME"
```

### Edge OAuth + Prod 中继（情形 A/B/C）

```bash
export TOKENKEY_PROD_BASE_URL="https://api.tokenkey.dev"
export TOKENKEY_PROD_ADMIN_API_KEY="admin-REPLACE_ME"

export TOKENKEY_EDGE_BASE_URL="https://api-us6.tokenkey.dev"   # 也可只在 JSON 写 edge_id
export TOKENKEY_EDGE_ADMIN_API_KEY="admin-REPLACE_ME"
```

Admin API Key 位置：后台 → **系统设置 → 安全与认证 → 管理员 API Key**。  
**勿把真实 token / admin key / tk_ key 提交 Git。**

---

## JSON 怎么写

### 标准中继模板（推荐从这里抄）

| 平台 | 示例文件 |
|------|---------|
| Anthropic OAuth | `examples/edge-oauth-relay-anthropic-us6.json` |
| Antigravity OAuth | `examples/edge-oauth-relay-antigravity-us6.json` |
| OpenAI / Codex | `examples/edge-oauth-relay-openai-us6.json` |
| Grok OAuth | `examples/edge-oauth-relay-grok-us6.json` |
| Kiro OAuth | `examples/edge-oauth-relay-kiro-us6.json` |

关键字段：

| 字段 | 含义 |
|------|------|
| `edge_id` | edge 节点，如 `us6` → `api-us6.tokenkey.dev` |
| `pool_platform` | 调度池类型，决定 prod 找哪条 stub |
| `edge_oauth` | edge 上要建/更新的 **真 OAuth** |
| `prod_relay` | prod 上要建的 **mirror stub**（若已存在则跳过） |
| `prod_relay.edge_api_key_user_id` | edge 上给谁签发 `tk_...`（自动签发时用） |
| `prod_relay.edge_api_key` | 手工指定已有 `tk_...`（与上一行二选一） |

可选：`edge_api_key_name` 自定义 key 名称（默认 `relay-{pool}-{edge_id}`）；同名 key 默认复用。

### 其他快捷格式

| 场景 | 识别方式 |
|------|---------|
| Antigravity 导出 JSON | `type: antigravity` 或带 `access_token` + `project_id` |
| Codex session | `import_profile: codex_session` 或 `accessToken` 等字段 |
| Grok SSO 批量 | `import_profile: grok_sso` + `sso_tokens` |
| 普通单账号 | `name` + `platform` + `type` + `credentials` |

### 重复导入会怎样

- 去重看 **OAuth 凭据**（refresh_token / email 等），**不看**显示名 `name`
- 默认 `update_existing: true` → 同凭据 **更新** 已有账号
- 同名但凭据不同 → **新建** 一条（不会按 name 覆盖）

---

## 注意事项

- 模板在 `examples/`，凭据占位符都是 `REPLACE_ME`
- 导入前务必 `--dry-run`，确认 `prod_action` 是 `create` 还是 `skip`
- prod 新建 stub 时，edge 签发的 key 必须绑对 **分组**（`edge_api_key_group_id`），否则 prod 转发过去 edge 调度不到你的 OAuth 账号
