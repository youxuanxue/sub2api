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
   （只读）               （写 Go allowlist）      （写 manifest/migration/价） （写 prod/edge settings）
```

| 运营说法 / 症状 | 走哪条分支 |
| --- | --- |
| 目录/Menu 过时、模型可能不再 200、要刷新 allowlist | **分支 B**（本 skill 路由后读写入子 skill） |
| Antigravity `gemini-2.5-pro` generateContent 超时 / inconclusive，要窄探 chat vs v1beta | **分支 B** → `tokenkey-servable-model-refresh` §「Antigravity gemini-2.5-pro 专项」 |
| OpenAI 新 GPT 型号已定价但 prod `Unsupported model` / 可能被 `model_mapping` floor 拦 | 先按下方「OpenAI 新型号判读」做只读 prod + edge OAuth 账号探测；只有 edge/prod 账号实测 `servable` 后才进入 **分支 B/D** |
| Qwen/DeepSeek mapping 漂、429 空池、60↔72 mirror | **分支 A** |
| 已有 servable+priced+displayable SSOT，需要快速热更新账号 `model_mapping` | **分支 D**（runtime desired layer + 显式 check/diff/apply） |
| 客户要上新模型、ready_for_onboard | **分支 C**（可先 A 再 C） |
| 单账号单模型能不能通 | `tokenkey-account-model-probe`（诊断，非 hub 子分支） |

硬边界：**分支 A 只读**；分支 B/C/D 会改仓库或 prod/edge，**合并/apply/sync-runtime/clear-runtime 等人授权**。

设计基线：`docs/approved/served-model-reconcile-planner.md` · 脚本表：`ops/pricing/README.md`

---

## 0) 路由（先做，再跑命令）

1. 问清是 **catalog/menu**、**newapi mapping/镜像**、**账号 model_mapping runtime 热更新**、还是 **上新模型**。
2. catalog/menu → **§分支 B**，加载 `tokenkey-servable-model-refresh` 执行写入。
3. mapping/mirror/空池 → **§分支 A**（`modelops.py plan`）。
4. runtime 热更新 → **§分支 D**，先 `validate/check --file`，确认后 `sync-runtime`（只写 setting）；账号持久化写入必须再跑 `check-accounts` 看 diff，确认后 `apply-accounts --confirm ...`。
5. plan 出 `ready_for_onboard` → **§分支 C**，加载 `tokenkey-onboard-model`。

同一工单可 A→C 或「B 与 A 并行认知、分开 PR」；**禁止**在分支 A 里跑 refresh `run/apply`。

### OpenAI 新型号判读（prod floor vs edge OAuth 上游）

OpenAI native/OAuth 型号不能只看「已定价」或 prod 普通探测的 400。prod 的 OpenAI catalog/Menu
与账号 `model_mapping` floor 会先拦住未列入经验可服务集合的型号，表现为
`Unsupported model: <id>`、`account_id=null`、无 upstream event；这只能说明当前 prod serving
面未放行，**不等于** edge OAuth 上游账号可服务。反过来，清开 prod floor 后也不保证能服务：
`gpt-5.6` 在 2026-07-08 的实测中，edge OpenAI OAuth account 7/5 都到达上游但返回
`The 'gpt-5.6-sol' model is not supported when using Codex with a ChatGPT account.`。

安全顺序：

```bash
# 1) 当前 prod/menu 真值：普通 catalog probe；若 400 Unsupported/account_id=null，先当作 floor 拦截
bash ops/observability/run-probe.sh --target prod \
  --script ops/pricing/probe-servable-models.sh \
  --with ops/pricing/probe_reserved_resources.sh \
  --env "OPENAI_RESPONSES_MODELS=<model>" --timeout-seconds 180

# 2) 账号能力真值：挑 deployable edge 或 prod 上的 OpenAI OAuth 账号单账号探测
bash ops/observability/run-probe.sh --target edge:<edge_id> \
  --script ops/stage0/probe_account_model.sh \
  --with ops/pricing/probe_reserved_resources.sh \
  --env ACCOUNT_ID=<openai_oauth_account_id> \
  --env MODEL=<model> --env ENDPOINT=responses --timeout-seconds 180
```

只有单账号探测返回 `verdict=servable` 且 usage 命中目标账号，才允许把该型号当作
servable 候选进入 **分支 B**（公开 catalog/Menu）或 **分支 D**（runtime model_mapping）。若
`verdict=upstream_rejected` 且错误为 `not supported when using Codex with a ChatGPT account`，保持不展示、不热更
`model_mapping`；若 `verdict=gateway_rejected` 且 body 是 `Unsupported model: <id>`，仍需换 edge/prod
账号或检查 floor，不能当作上游能力结论。

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

## 分支 D：账号 `model_mapping` runtime 热更新（desired layer + 显式 apply）

脚本：`ops/pricing/manage-account-model-mapping-runtime.py`。

用途：把已确认 **可服务、已定价、可展示** 的账号 `model_mapping` SSOT 作为 runtime
replacement 写入 `settings.tk_account_model_mapping_runtime`，再用只读 `check-accounts`
对 prod 生成 diff（默认 prod only；edge 空 mapping 不纳入门禁，需 `--include-edges` 才查 edge）。账号和 Antigravity group 的持久化写入只通过
`apply-accounts --confirm yes-apply-account-model-mapping` 执行；服务进程启动、周期 tick
和 `settings_updated` fan-out 都不会批量覆盖账号配置。该文件是 **scope replacement**，
不是增量 patch：写了某个平台或 newapi channel_type，就必须给出该 scope 的完整期望
mapping；未出现的 scope 继续用编译期 floor。

```bash
python3 ops/pricing/manage-account-model-mapping-runtime.py --selftest
python3 ops/pricing/manage-account-model-mapping-runtime.py example > /tmp/account-model-mapping-runtime.json
python3 ops/pricing/manage-account-model-mapping-runtime.py validate --file /tmp/account-model-mapping-runtime.json
python3 ops/pricing/manage-account-model-mapping-runtime.py check --file /tmp/account-model-mapping-runtime.json

# 人审 JSON + check 输出后再写 prod + deployable edge settings（不改 accounts）：
python3 ops/pricing/manage-account-model-mapping-runtime.py sync-runtime --file /tmp/account-model-mapping-runtime.json

# 发版后 / 热更新后只读 diff（默认 prod only）：
python3 ops/pricing/manage-account-model-mapping-runtime.py check-accounts --json

# 人审 diff 后，显式覆盖账号 model_mapping / Antigravity group scopes：
python3 ops/pricing/manage-account-model-mapping-runtime.py apply-accounts \
  --target all-deployable-and-prod \
  --confirm yes-apply-account-model-mapping

# 回到编译期 floor（prod + deployable edges，也需人审）：
python3 ops/pricing/manage-account-model-mapping-runtime.py clear-runtime
```

新增模型的安全顺序：先确认 live probe / pricing / display gate，再更新 runtime JSON，
`sync-runtime` 后跑 `check-accounts` 生成 diff，最后经人审 `apply-accounts`。如果这是长期
产品面，随后把同样的 mapping 折回 Go floor 或 `tk_served_models.json`，避免 runtime
长期 shadow 编译期事实。

---

## 四事实（hub 只对齐，不拥有）

| 事实 | Owner | 典型分支 |
| --- | --- | --- |
| Public catalog + Menu | `pricing_catalog_supported_models_tk.go` | B |
| Runtime serving | `accounts.credentials.model_mapping` + `tk_account_model_mapping_runtime` | A / C / D |
| Price | overlay + channel pricing | A / C / hotfix |
| Curated newapi intent | `tk_served_models.json` | A / C |
