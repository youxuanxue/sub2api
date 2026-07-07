---
name: tokenkey-modelops-planner
description: >-
  TokenKey model operations hub — single ops entry for catalog/menu refresh, newapi
  mapping drift, mirror diff, account model_mapping runtime hotfix, and onboard prep.
  Routes read-only plan, catalog write, runtime setting sync, or onboard write.
---

# TokenKey：modelops 运营 hub（唯一入口）

**所有 model ops 从这里进**——公开 `/pricing`、用户 Menu、newapi 长尾 mapping、mirror、上架前
对账。不要直接加载写入子 skill，除非本 skill 路由表已判定意图。

```text
                    tokenkey-modelops-planner（本 skill）
                              │
         ┌────────────────────┼────────────────────┬────────────────────┐
         ▼                    ▼                    ▼                    ▼
   分支 A 对账            分支 B catalog/menu     分支 C 上架           分支 D runtime
   modelops.py plan      refresh-servable-*      tokenkey-onboard-model manage-account-model-mapping-runtime.py
   （只读）               （写 Go allowlist）      （写 manifest/migration/价） （写 prod settings）
```

| 运营说法 / 症状 | 走哪条分支 |
| --- | --- |
| 目录/Menu 过时、模型可能不再 200、要刷新 allowlist | **分支 B**（本 skill 路由后读写入子 skill） |
| Antigravity `gemini-2.5-pro` generateContent 超时 / inconclusive，要窄探 chat vs v1beta | **分支 B** → `tokenkey-servable-model-refresh` §「Antigravity gemini-2.5-pro 专项」 |
| Qwen/DeepSeek mapping 漂、429 空池、60↔72 mirror | **分支 A** |
| 已有 servable+priced+displayable SSOT，需要快速热更新所有账号 `model_mapping` | **分支 D**（runtime settings 热更新） |
| 客户要上新模型、ready_for_onboard | **分支 C**（可先 A 再 C） |
| 单账号单模型能不能通 | `tokenkey-account-model-probe`（诊断，非 hub 子分支） |

硬边界：**分支 A 只读**；分支 B/C/D 会改仓库或 prod，**合并/apply/sync-runtime/clear-runtime 等人授权**。

设计基线：`docs/approved/served-model-reconcile-planner.md` · 脚本表：`ops/pricing/README.md`

---

## 0) 路由（先做，再跑命令）

1. 问清是 **catalog/menu**、**newapi mapping/镜像**、**账号 model_mapping runtime 热更新**、还是 **上新模型**。
2. catalog/menu → **§分支 B**，加载 `tokenkey-servable-model-refresh` 执行写入。
3. mapping/mirror/空池 → **§分支 A**（`modelops.py plan`）。
4. runtime 热更新 → **§分支 D**，先 `validate/check --file`，确认后再 `sync-runtime`。
5. plan 出 `ready_for_onboard` → **§分支 C**，加载 `tokenkey-onboard-model`。

同一工单可 A→C 或「B 与 A 并行认知、分开 PR」；**禁止**在分支 A 里跑 refresh `run/apply`。

---

## 分支 A：newapi / mapping / mirror 对账（只读）

脚本：`ops/pricing/modelops.py`（`reconcile-served-models.py` 为兼容 wrapper）。

```bash
python3 ops/pricing/modelops.py --selftest   # 可选，本地

python3 ops/pricing/modelops.py snapshot-sql --accounts 60,72
# → 经 prod 只读 DB 通道存 JSON

bash ops/observability/run-probe.sh --target prod \
  --script ops/pricing/probe-servable-models.sh \
  --with ops/pricing/probe_reserved_resources.sh \
  --env "DASHSCOPE_CHAT_MODELS=qwen3-8b" --timeout-seconds 300 \
  | tee /tmp/qwen_probe.tsv

python3 ops/pricing/modelops.py plan \
  --upstream 60:/tmp/qwen_upstream.json \
  --probe-results /tmp/qwen_probe.tsv \
  --live-mapping /tmp/model_mapping_snapshot.json \
  --mirror 60:72
```

| plan 块 | 下一步 |
| --- | --- |
| `probe_needed` | 复制 `probe_commands` |
| `price_missing` | `apply-pricing-hotfix.py lookup` 或 **分支 C** |
| `mapping_*` / `mirror_*` | guarded dry-run → 人审 → apply-live |
| `ready_for_onboard` | **§分支 C** |
| `surfaces.catalog_menu` | 若目录也 stale → **§分支 B**（与 mapping 不同 PR 面） |

策展账号 id：60 Qwen、72 Qwen-2（mirror 源→目标）、39 ds-官、67 GLM、7 volcengine。

---

## 分支 B：公开 catalog / Menu 实测刷新（写入）

**执行体 = `tokenkey-servable-model-refresh`**（Gemini 三族、Volcengine ark、inconclusive
取舍、catch-all 安全闸等细节只在该 skill，此处不重复）。

Hub 级最小路径（需 AWS / prod SSM）：

```bash
# 预览候选（无需 prod）
python3 ops/pricing/refresh-servable-allowlist.py candidates

# 探测 → 重写 pricing_catalog_supported_models_tk.go
python3 ops/pricing/refresh-servable-allowlist.py run
# 或 probe | apply 分步；审 diff 后 PR；上线需发版

cd backend && go test -tags=unit ./internal/service/ -run PublicCatalog
```

路由后：**立即读** `.cursor/skills/tokenkey-servable-model-refresh/SKILL.md` 走完写入与真判断。
缺价告警处置仍用 `apply-pricing-hotfix.py`（refresh skill §姊妹 runbook）。

---

## 分支 C：newapi 长尾上架（写入）

**执行体 = `tokenkey-onboard-model`**。manifest → 迁移/overlay → livefire → PR。
不确定缺口时先 **分支 A** 再加载 onboard skill。

---

## 分支 D：账号 `model_mapping` runtime 热更新（写 prod settings）

脚本：`ops/pricing/manage-account-model-mapping-runtime.py`。

用途：把已确认 **可服务、已定价、可展示** 的账号 `model_mapping` SSOT 作为 runtime
replacement 写入 `settings.tk_account_model_mapping_runtime`，触发
`AccountModelMappingReconciler` 通过 `settings_updated` fan-out 或周期 tick 更新所有 active
账号。该文件是 **scope replacement**，不是增量 patch：写了某个平台或 newapi channel_type，
就必须给出该 scope 的完整期望 mapping；未出现的 scope 继续用编译期 floor。

```bash
python3 ops/pricing/manage-account-model-mapping-runtime.py --selftest
python3 ops/pricing/manage-account-model-mapping-runtime.py example > /tmp/account-model-mapping-runtime.json
python3 ops/pricing/manage-account-model-mapping-runtime.py validate --file /tmp/account-model-mapping-runtime.json
python3 ops/pricing/manage-account-model-mapping-runtime.py check --file /tmp/account-model-mapping-runtime.json

# 人审 JSON + check 输出后再写 prod：
python3 ops/pricing/manage-account-model-mapping-runtime.py sync-runtime --file /tmp/account-model-mapping-runtime.json

# 发版后 / 热更新后只读收敛检查（prod + deployable edges）：
python3 ops/pricing/manage-account-model-mapping-runtime.py check-accounts --json

# 回到编译期 floor（也需人审）：
python3 ops/pricing/manage-account-model-mapping-runtime.py clear-runtime
```

新增模型的安全顺序：先确认 live probe / pricing / display gate，再更新 runtime JSON；如果这是长期产品面，
随后把同样的 mapping 折回 Go floor 或 `tk_served_models.json`，避免 runtime 长期 shadow 编译期事实。

---

## 四事实（hub 只对齐，不拥有）

| 事实 | Owner | 典型分支 |
| --- | --- | --- |
| Public catalog + Menu | `pricing_catalog_supported_models_tk.go` | B |
| Runtime serving | `accounts.credentials.model_mapping` + `tk_account_model_mapping_runtime` | A / C / D |
| Price | overlay + channel pricing | A / C / hotfix |
| Curated newapi intent | `tk_served_models.json` | A / C |
