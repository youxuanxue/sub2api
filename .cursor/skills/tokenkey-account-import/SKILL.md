---
name: tokenkey-account-import
description: >-
  TokenKey ops account import hub — routes edge OAuth relay, single-endpoint
  imports, and Antigravity/Codex/Grok/Kiro profiles through ops/accounts/import-accounts.sh.
  Use when onboarding OAuth to edge+prod relay, importing API keys, or running
  validate/dry-run/import account workflows.
---

# TokenKey：运营账号导入（唯一 Agent 入口）

**所有「导入 TokenKey 账号」的 Agent 工单从这里进。** 行为 SSOT 是
`ops/accounts/import-accounts.py` + `ops/accounts/README.md`；本 skill 只做路由、
env 检查清单、命令模板，**不**在 prose 里重写导入逻辑。

```text
              tokenkey-account-import（本 skill）
                          │
        ┌─────────────────┼─────────────────┬──────────────────┐
        ▼                 ▼                 ▼                  ▼
   edge_oauth_relay   单端 create      antigravity 导出    codex / grok 快捷
   （prod+edge 双端）  （prod 或 edge）  （单端或 relay 内）  （单端或 relay 内）
        │                 │                 │                  │
        └─────────────────┴─────────────────┴──────────────────┘
                          │
              ./import-accounts.sh <子命令> …
```

| 运营说法 / 意图 | 走哪条 |
| --- | --- |
| 上 us6 anthropic / ag / grok OAuth，prod 还没有 cc-us6 / ag-us6 | **edge_oauth_relay 情形 A**（新建 prod stub + 自动签发 key） |
| prod 已有 cc-us6，只换 edge OAuth token | **edge_oauth_relay 情形 B**（只动 edge，prod 跳过） |
| 手里已有 edge 的 `tk_...`，不想自动签发 | **edge_oauth_relay 情形 C**（`prod_relay.edge_api_key`） |
| 只在 prod 或 edge 建 API Key / newapi / gemini 等 | **单端导入 情形 D** |
| 从 Antigravity 导出 JSON 导入 | `examples/antigravity-oauth-export.json` 或 relay 内 `edge_oauth` |
| 从 Claude cookie 在 edge 建 OAuth（不是本脚本） | 转 **`tokenkey-anthropic-oauth-cookie-edge`**，完成后再走 relay |

硬边界：**永远** `validate` → `--dry-run` → 人工确认 → `--yes`。禁止跳过 dry-run 直写。
禁止把 token / admin key / `tk_...` 打进 Git 或贴进聊天。

---

## 0) 路由（先做，再跑命令）

1. 问清：**单端**还是 **edge 真 OAuth + prod 中继**？
2. edge relay → 确认 `edge_id`、`pool_platform`（anthropic / openai / antigravity / grok / kiro）。
3. prod 是否已有同 edge+pool 的 stub？有 → 情形 B；无 → 情形 A 或 C。
4. 从 `ops/accounts/examples/edge-oauth-relay-<pool>-<edge>.json` 抄模板，替换 `REPLACE_ME`。
5. 执行下方命令；stdout 原样转发，不臆造 API 响应。

`gemini` **无** edge relay 路径 → 情形 D 单端。

---

## 1) 环境变量

**单端（情形 D）**

```bash
export TOKENKEY_BASE_URL="https://api.tokenkey.dev"
export TOKENKEY_ADMIN_API_KEY="admin-…"
```

**edge relay（情形 A/B/C）**

```bash
export TOKENKEY_PROD_BASE_URL="https://api.tokenkey.dev"
export TOKENKEY_PROD_ADMIN_API_KEY="admin-…"
export TOKENKEY_EDGE_BASE_URL="https://api-us6.tokenkey.dev"
export TOKENKEY_EDGE_ADMIN_API_KEY="admin-…"
```

Admin Key：后台 → **系统设置 → 安全与认证 → 管理员 API Key**。

---

## 2) 标准执行链（所有情形通用）

```bash
cd ops/accounts

./import-accounts.sh validate path/to/spec.json
./import-accounts.sh import path/to/spec.json --dry-run
# 人工确认 prod_action=create|skip、edge 路由、edge_api_key_issue
./import-accounts.sh import path/to/spec.json --yes
```

辅助：

```bash
./import-accounts.sh list-edges
./import-accounts.sh list-platforms
./import-accounts.sh list-channel-types   # newapi 专用
./import-accounts.sh selftest
```

---

## 3) 情形 A — 全新 edge OAuth + prod 中继（推荐自动签发 key）

模板：`examples/edge-oauth-relay-<pool>-us6.json`（把 `us6` 换成目标 edge）。

JSON 要点：

```json
{
  "import_profile": "edge_oauth_relay",
  "edge_id": "us6",
  "pool_platform": "anthropic",
  "edge_oauth": { "...": "真 OAuth 凭据" },
  "prod_relay": {
    "name": "cc-us6",
    "group_ids": [1],
    "edge_api_key_user_id": 1,
    "edge_api_key_group_id": 1
  }
}
```

dry-run 输出里关注：`prod_action=create`、`edge_api_key_issue.action` 为 `would_create` 或 `reused`。

---

## 4) 情形 B — prod stub 已存在，只更新 edge OAuth

同上 JSON，**不必**改 `prod_relay` 名字；dry-run 应显示 `prod_action=skip`。

---

## 5) 情形 C — 手工指定 edge `tk_...`

```json
"prod_relay": {
  "name": "cc-us6",
  "group_ids": [1],
  "edge_api_key": "tk_已在_edge_签好的_key"
}
```

---

## 6) 情形 D — 单端导入

```bash
./import-accounts.sh validate examples/anthropic-apikey.json
./import-accounts.sh import examples/anthropic-apikey.json --dry-run
./import-accounts.sh import examples/anthropic-apikey.json --yes
```

可选 `--prod-base-url` / `--edge-base-url` 覆盖默认 env（见 `import-accounts.sh --help`）。

---

## 7) 与相邻 skill 的分工

| 主题 | 用谁 |
| --- | --- |
| 账号导入 / edge relay | **本 skill** + `ops/accounts/` |
| Claude cookie → edge OAuth | `tokenkey-anthropic-oauth-cookie-edge` |
| catalog / model_mapping / 上架模型 | `tokenkey-modelops-planner` |
| 单账号单模型能不能通 | `tokenkey-account-model-probe` |

---

## 8) 读完 dry-run 怎么判

| 字段 | 含义 |
| --- | --- |
| `prod_action=skip` | prod 已有同 edge+pool stub，符合情形 B |
| `prod_action=create` | 将新建 prod mirror stub |
| `edge_api_key_issue.action=reused` | 复用 edge 上同名 key |
| `edge_api_key_issue.action=would_create` | dry-run；真跑会 POST `/admin/users/:id/api-keys` |
| OAuth 重复 | 同凭据 **更新**（默认 `update_existing: true`），不看 name |

详细架构与 A/B/C/D 说明：`ops/accounts/README.md`。
