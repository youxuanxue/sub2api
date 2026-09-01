---
name: tokenkey-modelops-planner
description: >-
  TokenKey model operations hub — single ops entry for catalog/menu refresh, newapi
  mapping drift, mirror diff, account model_mapping runtime hotfix, and onboard prep.
  Routes read-only plan, catalog write, runtime setting sync, or onboard write.
---

# TokenKey：modelops 运营 hub（唯一入口）

**所有 model ops 从这里进**。命令、脚本表与契约细节只维护在
`ops/pricing/README.md`；本 skill 只做路由与判读。不要直接加载写入子 skill，除非本表已判定意图。

```text
                    tokenkey-modelops-planner（本 skill）
                              │
         ┌────────────────────┼────────────────────┬────────────────────┐
         ▼                    ▼                    ▼                    ▼
   分支 A 对账            分支 B catalog/menu     分支 C 上架           分支 D runtime
   modelops.py plan      refresh-servable-*      tokenkey-onboard-model manage-account-model-mapping-runtime.py
   （只读）               （写 Go allowlist）      （写 manifest/migration/价） （写 prod/edge settings）
```

| 运营说法 / 症状 | 走哪条分支 |
| --- | --- |
| 目录/Menu 过时、模型可能不再 200、要刷新 allowlist | **分支 B** → `tokenkey-servable-model-refresh` |
| Antigravity `gemini-2.5-pro` generateContent 超时 / inconclusive | **分支 B** → refresh skill §「Antigravity gemini-2.5-pro 专项」；命令见 `ops/pricing/README.md` |
| 新模型已定价/可展示但 prod `Unsupported model`、空池或疑似被 `model_mapping` floor 拦 | 先做下方「floor vs 上游能力」判读；实测 `servable` 后再进 **B/D** |
| Qwen/DeepSeek mapping 漂、429 空池、60↔72 mirror | **分支 A** |
| 已复核证据，要热更新账号 `model_mapping` | **分支 D** |
| 已有 release bundle，正式激活到 prod | `modelops.py activate`（见 README） |
| 客户要上新模型、`ready_for_onboard` | **分支 C**（可先 A） |
| 单账号单模型能不能通 | `tokenkey-account-model-probe`（诊断，非 hub 子分支） |

硬边界：**分支 A 只读**；分支 B/C/D 会改仓库或 prod/edge，**合并/apply/sync-runtime/clear-runtime 等人授权**。

设计基线：`docs/approved/served-model-reconcile-planner.md` · 交付公式：`docs/approved/pricing-serving-single-source-of-truth.md` · 脚本表：`ops/pricing/README.md`

测试：catalog/menu/model_mapping/pricing 的正负样本必须从 canonical owner 派生；禁止手写会随上架漂移的模型清单。

---

## 0) 路由

1. 问清是 **catalog/menu**、**mapping/镜像**、**runtime 热更新**、还是 **上新模型**。
2. catalog/menu → **分支 B**，加载 `tokenkey-servable-model-refresh`。
3. mapping/mirror/空池 → **分支 A**（`modelops.py plan`）。
4. runtime → **分支 D**：`validate/check` → 人审 → `sync-runtime`；账号持久化再 `check-accounts` → `apply-accounts --confirm ...`。
5. `ready_for_onboard` → **分支 C**，加载 `tokenkey-onboard-model`。

同一工单可 A→C；**禁止**在分支 A 里跑 refresh `run/apply`。

### 新模型：prod floor vs 上游能力（真判断）

CatalogPolicy 细节见 `docs/global/agent-reference.md` § Model serving SSOT。

- prod 普通探测的 `Unsupported model` / `account_id=null` / 空池 → **只说明当前 floor/mapping 未放行**，不是上游不可服务。
- 热更 mapping 后仍须看到目标账号路径 `verdict=servable`。
- 安全顺序（命令全文只在 `ops/pricing/README.md` + `tokenkey-account-model-probe`）：
  1. catalog/gateway probe（当前 serving 面）
  2. `probe_account_model`（目标账号经 TK）
  3. `probe_direct_upstream_model` / 平台专用直连（绕过 TK floor）
- 仅当上游/平台探测 `servable`，且 gateway 单账号探测 `usage_match.account_id == ACCOUNT_ID`，才进 **B/D**。
- 上游已到达但 `upstream_rejected` → 不展示、不热更 mapping。

---

## 分支 A：newapi / mapping / mirror 对账（只读）

入口：`python3 ops/pricing/modelops.py plan …`（`reconcile-served-models.py` 仅为兼容 wrapper）。
参数、snapshot、probe 示例见 `ops/pricing/README.md`。

| plan 块 | 下一步 |
| --- | --- |
| `probe_needed` | 复制 `probe_commands` |
| `price_missing` | hotfix lookup 或 **分支 C** |
| `mapping_*` / `mirror_*` | dry-run → 人审 → apply |
| `ready_for_onboard` | **分支 C** |
| `surfaces.catalog_menu` | **分支 B**（另开 PR） |

策展账号锚点：60/72 Qwen mirror、39 DeepSeek、67 GLM、7 VolcEngine。

---

## 分支 B：catalog / Menu 刷新（写入）

加载 `tokenkey-servable-model-refresh`。Hub 最小入口：

```bash
python3 ops/pricing/refresh-servable-allowlist.py candidates
python3 ops/pricing/refresh-servable-allowlist.py run
cd backend && go test -tags=unit ./internal/service/ -run PublicCatalog
```

流量短路、`--skip-video`、Gemini/Ark 特例、verdict 语义：**只读 refresh skill + `ops/pricing/README.md`**，此处不重复。

---

## 分支 C：newapi 长尾上架（写入）

加载 `tokenkey-onboard-model`。不确定缺口时先 **分支 A**。

---

## 分支 D：`model_mapping` runtime（desired layer + 显式 apply）

脚本：`ops/pricing/manage-account-model-mapping-runtime.py`。完整命令与确认词见 `ops/pricing/README.md`。

必记边界（不复制命令块）：

- 默认检查 **prod only**；edge 空 mapping 不是 drift；`--include-edges` 仅显式排障。
- `sync-runtime` 只写 setting；账号持久化必须人审后 `apply-accounts --confirm yes-apply-account-model-mapping`。
- 文件是 **scope replacement**，不是增量 patch。
- 空 AG mapping：`platforms.antigravity` 叠编译期 `DefaultAntigravityModelMapping`；非空 mapping 仍以账号为准。
- 正式激活走 `modelops.py activate` + `docs/approved/model-surface-activation-contract.md`；generic deploy/rollback 不依赖这条链。

---

## 判定边界

交付公式只在 `docs/approved/pricing-serving-single-source-of-truth.md`。
本 hub 写 catalog / mapping / 价格证据，不写协议 route，也不写 runtime 容量。
