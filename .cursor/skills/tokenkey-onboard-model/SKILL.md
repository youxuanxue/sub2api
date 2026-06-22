---
name: tokenkey-onboard-model
description: >-
  TokenKey served+priced model onboarding workflow for curated newapi mapping accounts. Use when adding/pricing Qwen or DeepSeek long-tail models, serving a model via accounts 39/60, or debugging priced-but-empty-pool 429/503 drift.
---

# TokenKey：上架一个模型（served + priced，确定性流水线）

适用于本仓库（TokenKey fork of sub2api）。把"客户想用模型 X，要它在某账号上**可调用 + 计费正确**"
从一次性手工操作（裸 SQL 改 model_mapping、手补 overlay、靠记忆刷 scheduler_outbox）固化为可复跑、
有门禁兜底的分钟级流水线。

**单一意图源 = `backend/internal/service/tk_served_models.json`**（manifest）。它是一层**薄意图**，
声明"TK 在平台 P 上、经某账号的 `credentials.model_mapping` 白名单、以价格 π、display=是/否，服务模型 M"。
它**不替代**两个既有机制，只断言三方一致：

1. 账号 `credentials.model_mapping` —— 运行期"可服务白名单"，由 `tk_NNN_*model_mapping*.sql` 迁移
   （及 admin UI 编辑）写入；
2. `tk_pricing_overlay.json`（+ 运行期 litellm 镜像）—— 价格。

manifest 与 `tk_pricing_overlay.json` 同目录（`backend/internal/service/`），所以同一个 Go 包能 `//go:embed`
两者、同一个 preflight 能解析两者；选址理由见 manifest 头注 `_doc`。

## 范围（严格，越界=类别错误）

**只**覆盖 TK 策展、经账号 `model_mapping` 白名单服务的 **newapi 第五平台长尾**，当前两个专用单账号组：

| 账号 | 名称 | platform | channel_type | group | 上游 |
| --- | --- | --- | --- | --- | --- |
| 60 | Qwen | newapi | 17（Ali/DashScope） | 18 | `dashscope.aliyuncs.com`（裸 host，Ali 适配器自接 `/compatible-mode/v1`） |
| 39 | ds-官 | newapi | 43（DeepSeek） | 11 | `api.deepseek.com` |

**不含**：litellm 全目录；四个原生平台（anthropic / openai / gemini / antigravity，各有 Go allowlist map，
走 `tokenkey-servable-model-refresh`）；grok（原生第七平台 #791，platform=grok，经平台路由 + ch48 API-key
中继，**不**经 39/60 的 model_mapping —— 把 grok 写进 manifest 会让 `served_on` 失去意义）。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审：

- **机械化（脚本承载）**：上游可服务性 probe（`probe-servable-models.sh` 的 dashscope/qwen 与 ark/deepseek
  族）、价格落 overlay（`apply-pricing-hotfix.py stage-overlay`，从官方源/litellm 取价）、渠道热更
  （`apply-pricing-hotfix.py apply`）、三方一致校验（`scripts/checks/catalog-serving-drift.py`，经
  `scripts/preflight.sh` 调用）、livefire 200 复测（probe 族）。
- **真判断（留给人/agent）**：① 官方价是多少、有没有阶梯/思考双档（**禁臆造**，必须查官方页带 `source`
  URL+抓取日）；② `inconclusive`（非空池 429/502/503）是"该组真没这账号"还是"模型本身不可用"（空池 429+"No
  available accounts" 已由脚本机械判为 `not_allowlisted`，不属此项真判断）；③ 该不该清空/
  catch-all（见 servable-refresh skill 的安全闸）；④ 合并授权（人）。

## 流程（每步确定性；脚本承载可机械化部分）

### 0) 前置事实（一次性确认，别跳）

- manifest 的 `served_on` 直指账号 id（字符串）。**先确认该 model_mapping 是迁移写的还是账号种子态**：
  qwen3.7-max 家族、deepseek-v4-pro/flash 的白名单是**原始账号种子态**（admin UI / 初始 seed），
  `tk_021`/`tk_022` 只翻 platform/dispatch、**不**写这些 model_mapping 字面量；tk_024/tk_027 用 `||` 往
  既有白名单**追加**。这决定 manifest 条目要不要带 `served-via-admin-ui` 标记（见 §安全网）。
- 价格基准：DashScope 用**中国大陆（北京）RMB 列表价 ÷ 6.7**（canonical CNY/USD），国际部署价更高、
  prod 账号走大陆 endpoint故**不**建模国际价。思考/非思考双档：开源 dense Qwen3（8b/14b/32b）
  `enable_thinking` 默认 true，输出默认按思考档计费，overlay 要带 `thinking_output_cost_per_token`
  （pricing-overlay.py 有 THINKING_ANCHORS 硬门禁）。

### 1) probe 上游可服务性（dashscope/qwen 族）

经 prod/edge SSM 用账号自身 key 发一条最小真实请求读 HTTP 状态。dashscope 经 newapi 网关探（账号 60 在
group 18），与 servable-refresh 的 gemini 族同形（OpenAI-compat /v1/chat/completions），thinking 与非
thinking 各发一条确认两档都 200：

```bash
# 经 prod 网关、用 group 18 绑定的 api_key 探 newapi/dashscope（参照 GEMINI_* 族的 group-key 取法）
bash ops/observability/run-probe.sh --target prod --script ops/pricing/probe-servable-models.sh \
  --env "DASHSCOPE_CHAT_MODELS=qwen3-8b qwen3-14b qwen3-32b" --timeout-seconds 300
# 200=servable（留）；400/404+not-found/retired=unsupported；429+"No available accounts"=not_allowlisted（TK 空池=未在调度层 allowlist，#812 信号）；其它 429/502/503=inconclusive；401/403=auth_error
```

> **注**：dashscope/qwen probe 族（`DASHSCOPE_CHAT_MODELS` → group `Qwen`(18) 绑定的 api_key →
> `/v1/chat/completions`，思考档 `enable_thinking:true`、非思考档须显式 `enable_thinking:false`）。
> `DASHSCOPE_GROUP_NAME` 默认**已是 `Qwen`**（大小写敏感，prod 实证；曾因默认小写 `qwen` 与真组 `Qwen`
> 不匹配吞掉一次 livefire），无需再手传组名；组名/key 取不到时探针报 `config_error`（带 stderr 诊断），
> **不再伪装成 `auth_error`**。**此族属本 skill 的 column-3 配套**，与 manifest/guard 解耦——guard 不依赖
> probe 跑过，只校验仓库内三方一致。

### 2) 写 manifest 条目（单一意图源，**先于**投影）

往 `backend/internal/service/tk_served_models.json` 的 `entries` 加一条，key=`<platform>/<model_id>`：

```json
"newapi/qwen3-8b": {
  "platform": "newapi", "model_id": "qwen3-8b", "served_on": ["60"],
  "channel_type": 17, "price_source": "overlay", "price_key": "qwen3-8b",
  "display": false,
  "notes": "Qwen account 60. <provenance / known-gap>"
}
```

字段语义见 manifest 头注 `_schema`。要点：

- `display` 对 newapi **必须 false**（newapi 无 Go servable-allowlist map，公开目录靠价格存在透传）。
  `display=true` 只对有 map 的平台（anthropic/openai/gemini/antigravity）合法。
- `price_source`：`overlay`（在 overlay 有非零价）/ `mirror`（litellm 镜像已带非零价、overlay 故意不收，
  如 deepseek-chat/reasoner）/ `channel`（渠道定价 DB）。
- **若 model_mapping 是账号种子态而非迁移写的**（qwen3.7-max / deepseek-v4-*），`notes` 必须含字面
  `served-via-admin-ui`，否则 §安全网 A3 会硬失败。

### 3) 投影：迁移 + overlay 价

**(a) model_mapping 迁移**（`served_on` 方向的真值）。模板取 `tk_027`（`jsonb || ` 追加 identity 白名单
+ `scheduler_outbox account_changed`，幂等 + 跨部署安全，按 `(id,name,platform,channel_type)` 守卫）。
新文件 `backend/migrations/tk_NNN_<name>_model_mapping.sql`：

```sql
WITH upd AS (
    UPDATE accounts
    SET credentials = jsonb_set(credentials, '{model_mapping}',
            COALESCE(credentials -> 'model_mapping', '{}'::jsonb) || '{
                "qwen3-8b": "qwen3-8b", "qwen3-14b": "qwen3-14b", "qwen3-32b": "qwen3-32b"
            }'::jsonb),
        updated_at = NOW()
    WHERE id = 60 AND name = 'Qwen' AND platform = 'newapi' AND channel_type = 17
      AND deleted_at IS NULL
    RETURNING id
)
INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
SELECT 'account_changed', id, NULL, NULL FROM upd;
```

裸 SQL 改账号绕过 Ent snapshot 刷新钩子，**必须**enqueue `scheduler_outbox`（否则运行中的副本保留旧白名单）。

**(b) overlay 价**（fill-only，**禁臆造**）。查官方价后用 hotfix 工具固化：

```bash
python3 ops/pricing/apply-pricing-hotfix.py lookup --model qwen3-8b           # litellm 全量源取价
python3 ops/pricing/apply-pricing-hotfix.py stage-overlay --model qwen3-8b --from-litellm  # 进 overlay,提 PR
# 官方源未收录则用 --entry-json 手填（带真实 source URL+抓取日；思考双档加 thinking_output_cost_per_token）
```

overlay 是 fill-only：源带非零价时源胜、overlay 被忽略；**不能纠正错的非零源价**（deepseek-chat/reasoner
镜像仍是 pre-V4 价 → 用 `price_source=mirror`，错价要修走渠道定价 DB 而非 overlay）。

### 4) apply-live（零发版）

- **model_mapping（两条路径）**：①**常规**——迁移随发版部署，迁移体内 `scheduler_outbox account_changed`
  让运行中的调度器热加载（部署即生效）。②**零发版热更**——用
  `python3 ops/newapi/apply-model-mapping-live.py sync-live --account-id N --name X --channel-type T
  --add-identity <model_id>`：guard 锁 `id+name+platform+channel_type+deleted_at` 的幂等 `jsonb ||` 合并
  + `scheduler_outbox account_changed` + BEFORE/AFTER 复核，经 prod SSM 直接打到 DB（下次发版迁移再跑是
  no-op）。先 `check --account-id N` 只读看现状、`--dry-run` 预览。该工具已内建两条铁律：裸 SQL 改账号
  **必须**同时 enqueue `scheduler_outbox`（否则运行副本保留旧白名单，memory `gemini_media`）；SSM 脚本含
  `docker exec -i` 必须**写文件再执行**、勿管道喂 `bash`（否则首个 psql 吞掉脚本后半段还报 Success，memory
  `feedback_docker_exec_gotchas`）。
- **overlay 价（热推，不等发版）**：内嵌 JSON 是**地板**，运行时活值经 settings `tk_pricing_overlay_runtime`
  **逐 key 叠加**在地板上（runtime 胜；未推的 key 仍走内嵌；空/坏 runtime 永不跌破地板——所以也可只推单个
  新 key 而不动其余）。PR 合并后跑 `python3 ops/pricing/manage-overlay-runtime.py sync-runtime`（先过
  `pricing-overlay.py` 门禁 → **gzip+base64 传输绕过 SSM 97KB 上限** → 在 Postgres 内
  `convert_from(decode(…,'base64'))` 写入、**避开 psql `:'v'` 在 `-c` 模式失效的坑** → UPSERT +
  `PUBLISH settings_updated` 即时跨副本重载；**prod-only**，edge 不跑计费）。模型立即被定价 + 出现在公开
  /pricing，**零镜像构建**。下次例行发版把 overlay 折进内嵌（地板追平），之后 `check` 应报「活值==内嵌」。
- **紧急渠道价**（仅 channel-priced 模型，凌驾一切）：`apply-pricing-hotfix.py apply --channel-id N`，立即生效。

### 5) livefire 复测（真 200）

apply 后用 §1 的 probe 族对 model_id 发**思考 + 非思考各一条**真实请求，确认都回 200（"定价就绪 ≠
prod 可服务"，见 memory `priced_not_equal_servable_verify_livefire`）：

```bash
bash ops/observability/run-probe.sh --target prod --script ops/pricing/probe-servable-models.sh \
  --env "DASHSCOPE_CHAT_MODELS=<model_id>"
# DASHSCOPE_GROUP_NAME 默认已是 Qwen，无需手传；期望两行皆 200 servable。
# 000 config_error = 组名/key 没取到（看 stderr 诊断），非模型信号、非上游 auth。
```

> 也可经 **TK universal key**（客户真路径）端到端验：`POST <prod>/v1/chat/completions`、`Authorization:
> Bearer <universal-key>`、`model=<model_id>`；universal key 按请求模型解析到 Qwen 组。非思考体须带
> `enable_thinking:false`，思考体须 `stream:true`（DashScope 硬约束）。

### 6) 两档计费核对 + 话术

- 拉真实 usage_log 核对。**注意**：`thinking_output_cost_per_token` 是 overlay 的**定价配置字段**，
  **不是** usage_logs 列——思考档在计费时切到该费率、结果仍写进同一 `output_cost` 列（无独立思考列）。
  usage_logs 实际列：`model` / `requested_model`（**计费键取 requested_model**）/ `input_tokens` /
  `output_tokens` / `input_cost` / `output_cost` / `cache_read_cost` / `total_cost`。核对
  `output_cost ≈ output_tokens × 实际档费率`（思考请求用 `thinking_output_cost_per_token`、非思考用
  `output_cost_per_token`；二者相等时无从区分、属正常）：

  ```sql
  SELECT created_at, model, requested_model, input_tokens, output_tokens,
         input_cost, output_cost, total_cost
  FROM usage_logs WHERE requested_model='<model_id>'
    AND created_at > now() - interval '30 min' ORDER BY id DESC LIMIT 6;
  ```
- 客户话术：模型名（DashScope 规范**小写** id）、endpoint（`/v1/chat/completions`）、计费档位
  （输入/输出/思考输出/缓存）、是否默认思考。

## 安全网：`scripts/checks/catalog-serving-drift.py`（漂移门禁，经 preflight）

manifest 编辑 + 迁移 + overlay 三件**必须先过门禁再提 PR**。仿 `scripts/checks/pricing-overlay.py` 约定
（`--quiet` / `--selftest`，exit 0 ok / 1 violation / 2 missing-dep）。校验：

- **A0 PARSE**：manifest 解析、`entries` 是对象、每条字段类型对。
- **A1 PRICE**（硬）：`overlay` 源 → price_key 在 overlay 且按 mode 字段 >0（复用 pricing-overlay.py 的
  MODE_FIELDS）；`mirror` 源 → 只证 overlay 没把它清零（静态看不到 live 镜像，这是故意偏弱、永不假阳的一臂）；
  `channel` 源 → notes 须含 `channel` 文档。
- **A2 DISPLAY ⇒ ALLOWLIST**（硬）：`display=true` 须在该平台的 Go servable-allowlist map（读
  `pricing_catalog_supported_models_tk.go` 的 `// servable-allowlist:begin/end <platform>` 标记块）。
  newapi 无 map → `display=true` 直接硬错。
- **A3 SERVED_ON ⇒ MIGRATION**（硬，**#812 捕获方向**）：每个 `served_on` 账号 A，model_id 的**双引号
  字面量**（`"<model_id>"`）须与账号守卫正则 `\bid\s*=\s*A\b` 在**同一 `tk_*.sql` 文件内共现**（file-level，
  读原始文本、**含注释**）。双引号防前缀假配（`"qwen3.7-max"` 不会命中 `"qwen3.7-max-preview"`）；file-level
  之所以不串味，是因为 seed 在 39/60 间不共享同一 model_id——若将来一文件把同名映上两账号、或某注释里出现
  带引号的 model_id 字面量，才需收紧到 per-`id`-语句作用域 + 去注释（guard docstring 已记此为待办，当前实现
  未做）。
  **逃逸阀**：notes 含字面 `served-via-admin-ui` → 该条 A3 降为 WARN（声明 model_mapping 是 admin/种子态、
  非迁移写——qwen3.7-max / deepseek-v4-* 即此类）。
- **A4 ENUMERATION**（WARN，advisory）：dashscope/deepseek 的 chat overlay 键无 manifest 条目→提示（可能漏；
  也可能是 dated 快照/proxy-fill 等合法非 manifest 行，故只 WARN）。

```bash
python3 scripts/checks/catalog-serving-drift.py --selftest   # 离线逻辑自检（preflight 跑）
python3 scripts/checks/catalog-serving-drift.py              # 校验仓库内 manifest↔migration↔overlay
```

> **#812 设计信号**：qwen3-8b/14b/32b 已在 overlay 定价（2026-06-17 客户请求）但**无**迁移把它们映上账号
> 60（最高 model_mapping 迁移是 tk_027）。manifest 把它们以 `served_on=["60"]` 种下，A3 就**硬失败**这三行，
> 在 CI 把"priced-but-not-mapped ⇒ 空池 429/503"的缺口顶到面上——直到 tk_029 落地或移除这三行。

接入 preflight：在 `scripts/preflight.sh` 追加一节（**勿改 dev-rules 模板**），紧邻既有
`=== sub2api: pricing overlay ===` 之后，同形调用 `--selftest` 后 `--quiet`。

## 运行期对账（column-2 SECONDARY，design-only，不门禁 PR）

静态 guard 证"仓库内 intent↔migration↔price 一致"；它**读不到 prod DB**。prod LIVE
`accounts.credentials.model_mapping` 是否真含 model_id 属另一只读 ops 扫描（永不门禁 PR）：

```sql
-- 只读；过滤 deleted_at（软删不重置 status，见 memory accounts_active_status_vs_deleted_at）
SELECT id, credentials->'model_mapping' FROM accounts WHERE id IN (39,60) AND deleted_at IS NULL;
```

与 manifest 的 `served_on`/`model_id` 做差集，作 observability 检查。

## 坑 / 判断要点

- **价格基准是大陆 RMB ÷ 6.7**，不是 USD 列表价 ÷ 7.27（2026-06-13 全表已从错基修正）。新条目对齐。
- **mapping 是迁移写的还是种子态**决定要不要 `served-via-admin-ui`——读迁移别只看文件名（tk_022 名为
  `_to_extension_engine` 却含 model_mapping 合并；qwen3.7-max/deepseek-v4-* 的白名单根本不在任何迁移里）。
- **零计费高量模型**：计费键取 `requested_model` 非 upstream_model，查 model_mapping_chain（见 memory
  `zero_cost_alias_hides_priced_upstream_model`）。
- **合并永远等人授权**；本 skill 不自动 `gh pr merge`。

## 姊妹 skill

- `tokenkey-servable-model-refresh`：四个原生平台（anthropic/openai/gemini/antigravity）的公开目录
  allowlist 实测刷新（有 Go map）+ ark/volcengine 运营对账。本 skill 是其在 **newapi 长尾 + 计费意图**
  方向的补集。
