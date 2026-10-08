---
name: tokenkey-edge-prod-relay-onboard
description: >-
  TokenKey full-chain edge OAuth pool + prod mirror stub onboard (create, capability
  repair, gateway probe). Use when wiring a new edge into prod relay for antigravity
  (implemented) or planning gemini-web/kiro/openai/anthropic placeholders.
---

# TokenKey：Edge 池 + Prod 中继全链路打通

**行为 SSOT**：`ops/accounts/edge-prod-relay-onboard.sh` + `relay_onboard_profiles.py`。  
本 skill 只做路由与检查清单，不在 prose 里重写实现。

与 `tokenkey-account-import` 的分工：

| 意图 | 入口 |
|---|---|
| 已有 OAuth 导出 / 单次 import JSON | `tokenkey-account-import` |
| **新 edge 从零打通**（分组、占位、relay key、prod stub、capability、实测） | **本 skill** |
| 单账号单模型归因探针 | `tokenkey-account-model-probe` |

当前 **仅 `antigravity` 已用实操+探针证据落地**；`gemini-web` / `kiro` / `openai` / `anthropic` 为 profile 占位，`apply` 会拒绝，待同样证据后补全。

---

## 0) 何时进入

- 「在 ukN/usN 上建 antigravity 占位 + prod `antigravity-ukN` 中继并实测」
- 「新 edge 打通某平台 oauth 池到 prod」
- 不要用本入口做 catalog/pricing（走 `tokenkey-modelops-planner`）

---

## 1) 环境

```bash
export TOKENKEY_PROD_ADMIN_API_KEY="admin-…"   # 或 TOKENKEY_ADMIN_API_KEY
# 边端二选一：
export TOKENKEY_EDGE_ADMIN_API_KEY="admin-…"   # 单 edge 时
# 或 apply/all 加 --fetch-edge-admin-key（SSM 读 settings.admin_api_key）
```

Admin Key：后台 → 系统设置 → 安全与认证 → 管理员 API Key。  
禁止把 admin key / `sk_` / token 写进 Git 或贴进聊天。

---

## 2) 标准命令

```bash
cd ops/accounts

./edge-prod-relay-onboard.sh list-platforms

./edge-prod-relay-onboard.sh plan  --platform antigravity --edge uk3
./edge-prod-relay-onboard.sh apply --platform antigravity --edge uk3 --fetch-edge-admin-key
# 确认 JSON 后
./edge-prod-relay-onboard.sh apply --platform antigravity --edge uk3 --fetch-edge-admin-key --yes

./edge-prod-relay-onboard.sh probe --platform antigravity --edge uk3
# 或
./edge-prod-relay-onboard.sh all --platform antigravity --edge uk1 --edge uk2 \
  --fetch-edge-admin-key --yes
```

硬边界：默认 dry-run；**必须**先 `plan`/`apply`（无 `--yes`）看清动作，再 `--yes`。`probe` 失败（非 `servable` 或 usage 未归因到该 stub）exit 1。  
多 edge 时禁止复用同一个 `TOKENKEY_EDGE_ADMIN_API_KEY` / `--edge-admin-key`，必须 `--fetch-edge-admin-key`。

---

## 3) Antigravity 全链路做了什么

对每个 `--edge`：

1. 确保 edge 分组 `antigravity`
2. 确保占位 OAuth `ag-{edge}-placeholder`（**关调度**）
3. 确保 relay API Key `relay-antigravity-{edge}`（绑 antigravity 分组）
4. 修复 edge `protocol_endpoint_capabilities`（空/`inconclusive` → `gemini_generate_content=positive`）
5. 确保 prod stub `antigravity-{edge}`（分组 Google-Vertex + Google-Antigravity；mapping/extra 对齐 `antigravity-us6`）
6. `probe`：`run-probe.sh` → `probe_account_model.sh`，`MODEL=gemini-3-flash` `ENDPOINT=gemini`，要求 `verdict=servable` 且 usage 归因到该 stub

实现细节（POST 经 `api.tokenkey.dev` 301 保 POST、SSM 取 admin key）在脚本内，skill 不复制。

---

## 4) 运营：占位换真号 / 加号

编辑弹窗**不能**手改 access/refresh。正确路径：

1. edge 后台 → 账号 → **重新授权**
2. **生成授权链接**（每次新会话；不按 account_id 定制，但 session 一次性）
3. 目标 Google 账号完成授权（尽量走该 Edge 出口）
4. 弹窗保持打开 → 粘贴完整回调 URL → 完成授权
5. **开启调度**；prod 中继不动

加号：复制账号 → 对新副本重复重新授权 → 开调度。

---

## 5) 扩展新平台（占位 → implemented）

在 `relay_onboard_profiles.py` 把对应 profile 从 `status: placeholder` 改为完整字段，并补：

- live `apply` 在一个真实 edge 上跑通
- `probe` servable 证据
- 平台特异 capability/mapping/分组命名
- 本 skill §3 式小结（只写差异，不复制脚本）

未证据前禁止把 placeholder 标成 implemented。

---

## 6) 自测

```bash
cd ops/accounts
python3 test_edge_prod_relay_onboard.py
```
