---
name: tokenkey-onboard-model
description: >-
  TokenKey served+priced model onboarding workflow for curated newapi mapping accounts. Use when adding or pricing Qwen, DeepSeek, Moonshot/Kimi, GLM, or VolcEngine long-tail models, serving via manifest-owned newapi accounts, or debugging priced-but-empty-pool 429/503 drift.
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

测试必须从 manifest / overlay / Go allowlist owner 派生集合断言；上架一个模型不应导致多处测试手写
正向/负向清单同步。手写测试样本只用于 SSOT 无法推导的边界（未知 ID、跨平台 ID、兼容别名、
priced-but-hidden 等），并在测试里标明边界含义。

## 范围（严格，越界=类别错误）

**只**覆盖 TK 策展、经账号 `model_mapping` 白名单服务的 **newapi 第五平台长尾**，包括这些专用账号/池：

| 账号 | 名称 | platform | channel_type | group | 上游 |
| --- | --- | --- | --- | --- | --- |
| 60 | Qwen | newapi | 17（Ali/DashScope） | 18 | `dashscope.aliyuncs.com`（裸 host，Ali 适配器自接 `/compatible-mode/v1`） |
| 39 | ds-官 | newapi | 43（DeepSeek） | 11 | `api.deepseek.com` |
| 83 | kimi | newapi | 25（Moonshot） | 19 | `api.moonshot.cn`（国内站 key；价格取国内官方 RMB 表） |

**不含**：litellm 全目录；四个原生平台（anthropic / openai / gemini / antigravity，各有 Go allowlist map，
走 `tokenkey-servable-model-refresh`）；grok（原生第七平台 #791，platform=grok，经平台路由 + ch48 API-key
中继，**不**经 39/60 的 model_mapping —— 把 grok 写进 manifest 会让 `served_on` 失去意义）。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审：

- **机械化（脚本承载）**：上游可服务性 probe（`probe-servable-models.sh` 的 dashscope/qwen 与 ark/deepseek
  族）、价格落 overlay（`apply-pricing-hotfix.py stage-overlay`，从官方源/litellm 取价）、渠道热更
  （`apply-pricing-hotfix.py apply`）、仓库内价格/展示/mapping 声明一致校验
  （`scripts/checks/catalog-serving-drift.py`，经 `scripts/preflight.sh` 调用）、bundle 生成与漂移校验、
  `modelops activate` 的 evidence/digest/plan/apply 门禁、livefire 200 复测（probe 族）。
- **真判断（留给人/agent）**：① 官方价是多少、有没有阶梯/思考双档（**禁臆造**，必须查官方页带 `source`
  URL+抓取日）；② `inconclusive`（非空池 429/502/503）是"该组真没这账号"还是"模型本身不可用"（空池 429+"No
  available accounts" 已由脚本机械判为 `not_allowlisted`，不属此项真判断）；③ 该不该清空/
  catch-all（见 servable-refresh skill 的安全闸）；④ 合并授权（人）。

## 流程（每步确定性；脚本承载可机械化部分）

### 0) 前置事实（一次性确认，别跳）

- manifest 的 `served_on` 直指账号 id（字符串）。新 mapping floor 一律在 notes 声明
  `served-via-modelops-activation`，并在 release 前走唯一写入口 `modelops activate`；generic deploy/rollback
  不写 live account mapping。`served-via-admin-ui` 只保留给 activation contract 之前已经存在的账号种子态
  （qwen3.7-max / deepseek-v4-*），旧 migration 字面量也仅作为历史证据兼容（见 §安全网）。
- 价格基准：DashScope 用**中国大陆（北京）RMB 列表价 ÷ 6.7**（canonical CNY/USD），国际部署价更高、
  prod 账号走大陆 endpoint故**不**建模国际价。思考/非思考双档：开源 dense Qwen3（8b/14b/32b）
  `enable_thinking` 默认 true，输出默认按思考档计费，overlay 要带 `thinking_output_cost_per_token`
  （pricing-overlay.py 有 THINKING_ANCHORS 硬门禁）。
- Moonshot 国内账号用 `platform.kimi.com/docs/pricing/*` 的 RMB 列表价 ÷ 6.7；禁止拿国际站 USD 表给
  `api.moonshot.cn` 账号定价。`moonshot-v1-auto` 当前经人审固定按 V1 128K 档计费，不自动按输入长度降档。

在 `tokenkey-onboard-model` 流程 §1 之前，若不确定缺口类型，先走 skill
`tokenkey-modelops-planner` 出只读 plan。

### 1) probe 上游可服务性（dashscope/qwen 族）

经 prod/edge SSM 用账号自身 key 发一条最小真实请求读 HTTP 状态。dashscope 经 newapi 网关探（账号 60 在
group 18），与 servable-refresh 的 gemini 族同形（OpenAI-compat /v1/chat/completions），thinking 与非
thinking 各发一条确认两档都 200：

```bash
# 经 prod 网关探 newapi/dashscope：probe key 由 __tk_probe_newapi_qwen_* 自动 ensure
# （从 PROBE_DASHSCOPE_SOURCE_GROUP 默认 `Qwen` 源组复制 schedulable 账号；--with 上传 companion 库）
bash ops/observability/run-probe.sh --target prod --script ops/pricing/probe-servable-models.sh \
  --with ops/pricing/probe_reserved_resources.sh \
  --env "DASHSCOPE_CHAT_MODELS=qwen3-8b qwen3-14b qwen3-32b" --timeout-seconds 300
# 200=servable（留）；400/404+not-found/retired=unsupported；429+"No available accounts"=not_allowlisted（TK 空池=未在调度层 allowlist，#812 信号）；其它 429/502/503=inconclusive；401/403=auth_error
```

> **注**：dashscope/qwen probe 族（`DASHSCOPE_CHAT_MODELS` → 经 prod 网关 `/v1/chat/completions`，
> 思考档 `enable_thinking:true`、非思考档须显式 `enable_thinking:false`）。probe key 走 reserved
> `__tk_probe_newapi_qwen_*`，从 `PROBE_DASHSCOPE_SOURCE_GROUP`（默认 **`Qwen`**，大小写敏感、prod 实证；
> 曾因默认小写 `qwen` 与真组 `Qwen` 不匹配吞掉一次 livefire）复制 schedulable 账号，无需手传组名；
> 源组/账号取不到时探针报 `config_error`（带 stderr 诊断），**不再伪装成 `auth_error`**。companion 库
> 必须随 `--with ops/pricing/probe_reserved_resources.sh` 上传。**此族属本 skill 的 column-3 配套**，
> 与 manifest/guard 解耦——guard 不依赖
> probe 跑过，只校验仓库内三方一致。

Moonshot/Kimi 先用账号保存路径相同的鉴权 `GET /v1/models` 取得候选，再对每个候选走账号隔离探测；
只有 `verdict=servable` 且 `usage_match.account_id` 命中目标账号才进入 manifest：

```bash
bash ops/observability/run-probe.sh --target prod \
  --script ops/stage0/probe_account_model.sh \
  --with ops/pricing/probe_reserved_resources.sh \
  --env ACCOUNT_ID=83 --env MODEL=<model_id> --env ENDPOINT=chat \
  --env MAX_TOKENS=1 --timeout-seconds 180
```

Moonshot 国内站与国际站 key/价格相互独立；账号 `base_url=api.moonshot.cn` 时，overlay 必须取
`platform.kimi.com/docs/pricing/*` 国内 RMB 表并按 TokenKey `CNY/USD=6.7` 换算。overlay 保存税前价，
billing、公开 `/pricing` 与 fallback 通过 overlay `_config.official_list_base_tax` 的 `moonshot` rule 统一叠加
配置中的 multiplier；provider、matcher、multiplier 都不得在 Go 或测试里维护第二份清单。

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

- `display` 对 newapi 由 manifest 直接拥有：实测可服务、已定价且完成/即将完成 activation 的公开模型用
  `true`；预热、下线或尚未开放的条目用 `false`。原生平台才额外要求 Go servable-allowlist map。
- `price_source`：`overlay`（在 overlay 有非零价）/ `mirror`（litellm 镜像已带非零价、overlay 故意不收，
  如 deepseek-chat/reasoner）/ `channel`（渠道定价 DB）。
- **新 mapping floor** 的 `notes` 必须含字面 `served-via-modelops-activation`；旧账号种子态
  （qwen3.7-max / deepseek-v4-*）保留 `served-via-admin-ui`。两者都不能替代 live activation/check，
  只让静态门禁知道预期写路径。

### 3) 投影：overlay 价 + release bundle

**(a) overlay 价**（fill-only，**禁臆造**）。查官方价后用 hotfix 工具固化：

```bash
python3 ops/pricing/apply-pricing-hotfix.py lookup --model qwen3-8b           # litellm 全量源取价
python3 ops/pricing/apply-pricing-hotfix.py stage-overlay --model qwen3-8b --from-litellm  # 进 overlay,提 PR
# 官方源未收录则用 --entry-json 手填（带真实 source URL+抓取日；思考双档加 thinking_output_cost_per_token）
```

overlay 是 fill-only：源带非零价时源胜、overlay 被忽略；**不能纠正错的非零源价**（deepseek-chat/reasoner
镜像仍是 pre-V4 价 → 用 `price_source=mirror`，错价要修走渠道定价 DB 而非 overlay）。

**(b) release bundle**。manifest/Go owner 改完后生成 checksummed target artifact；禁止手改生成 JSON：

```bash
cd backend
go run ./cmd/account-model-mapping bundle --output ../ops/pricing/model-surface-bundle.json
```

### 4) activation + apply-live

- **model_mapping（唯一新模型写路径）**：准备独立的 probe/pricing evidence JSON；两份证据都必须绑定当前与
  target bundle 的 `floor_sha256`、覆盖每个 added/retargeted mapping、时间不超过 24h，且 probe 来源与 pricing
  来源不同。先不带 confirm 运行 dry-run，审阅 prod plan 与 release gate；高风险审批通过后才加固定确认短语：

  ```bash
  python3 ops/pricing/modelops.py activate \
    --bundle ops/pricing/model-surface-bundle.json \
    --current-bundle /tmp/current-model-surface-bundle.json \
    --probe-evidence /tmp/model-activation-probe.json \
    --pricing-evidence /tmp/model-activation-pricing.json
  # 审批后：在同一命令末尾加 --confirm yes-activate-model-surface
  ```

  activation 只写 prod，保留兼容 extras，并在事务内拒绝会遮蔽 bundle 的 runtime mapping。必须在发布含 target
  bundle 的二进制前完成；generic deploy/rollback 永不代替这一步，也不要为新 floor 新建 mapping migration。
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
  --with ops/pricing/probe_reserved_resources.sh \
  --env "DASHSCOPE_CHAT_MODELS=<model_id>"
# PROBE_DASHSCOPE_SOURCE_GROUP 默认已是 Qwen，无需手传；期望两行皆 200 servable。
# 000 config_error = 源组/账号没取到（看 stderr 诊断），非模型信号、非上游 auth。
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

manifest 编辑 + overlay + mapping 路径声明三件**必须先过门禁再提 PR**。仿 `scripts/checks/pricing-overlay.py` 约定
（`--quiet` / `--selftest`，exit 0 ok / 1 violation / 2 missing-dep）。校验：

- **A0 PARSE**：manifest 解析、`entries` 是对象、每条字段类型对。
- **A1 PRICE**（硬）：`overlay` 源 → price_key 在 overlay 且按 mode 字段 >0（复用 pricing-overlay.py 的
  MODE_FIELDS）；`mirror` 源 → 只证 overlay 没把它清零（静态看不到 live 镜像，这是故意偏弱、永不假阳的一臂）；
  `channel` 源 → notes 须含 `channel` 文档。
- **A2 DISPLAY ⇒ OWNER**（硬）：原生平台 `display=true` 须在对应 Go servable-allowlist map；newapi 的 display
  由 manifest 自己拥有，不要求另一份 allowlist。
- **A3 SERVED_ON ⇒ MAPPING PATH**（硬，**#812 捕获方向**）：旧条目可由同一 `tk_*.sql` 内账号守卫 + quoted
  model id 证明历史 mapping；新 floor 必须在 notes 声明 `served-via-modelops-activation`，表示 live write 只经
  activation contract。该声明只证明静态写路径，不是 live 证明；release 前仍须携带独立证据完成 activation。
  activation contract 之前的账号种子态可保留 `served-via-admin-ui` legacy marker，并降为 WARN。
- **A4 ENUMERATION**（WARN，advisory）：dashscope/deepseek 的 chat overlay 键无 manifest 条目→提示（可能漏；
  也可能是 dated 快照/proxy-fill 等合法非 manifest 行，故只 WARN）。

```bash
python3 scripts/checks/catalog-serving-drift.py --selftest   # 离线逻辑自检（preflight 跑）
python3 scripts/checks/catalog-serving-drift.py              # 校验仓库内 manifest↔mapping-path↔overlay
```

> **#812 设计信号**：只定价/展示、不声明 mapping 写路径会让 prod 空池 429/503。A3 因此硬失败未被 legacy
> migration 覆盖、也没有 `served-via-modelops-activation` 的新 floor；声明后仍由 `modelops activate` 的 fresh
> evidence + prod gate 负责真正写入和闭环。

接入 preflight：在 `scripts/preflight.sh` 追加一节（**勿改 dev-rules 模板**），紧邻既有
`=== sub2api: pricing overlay ===` 之后，同形调用 `--selftest` 后 `--quiet`。

## 运行期对账（column-2 SECONDARY，design-only，不门禁 PR）

静态 guard 只证"仓库内 intent↔mapping path↔price 一致"；它**读不到 prod DB**。prod LIVE 对账统一走
`manage-account-model-mapping-runtime.py check-accounts --json --bundle <artifact>`；新增/retargeted floor 的写前、
写后硬门禁只走 `modelops activate`，不靠手写 SQL 扫描替代。

## 坑 / 判断要点

- **价格基准是大陆 RMB ÷ 6.7**，不是 USD 列表价 ÷ 7.27（2026-06-13 全表已从错基修正）。新条目对齐。
- 新 mapping floor 一律声明 `served-via-modelops-activation` 并走 activation；`served-via-admin-ui` 仅描述历史
  种子态，不可用于绕开新模型 evidence/confirm gate。
- **零计费高量模型**：计费键取 `requested_model` 非 upstream_model，查 model_mapping_chain（见 memory
  `zero_cost_alias_hides_priced_upstream_model`）。
- **合并永远等人授权**；本 skill 不自动 `gh pr merge`。

## 姊妹 skill

- **`tokenkey-modelops-planner`**（总入口）：上架/mapping 走 **分支 A**；catalog 走 **分支 B**。
- `tokenkey-servable-model-refresh`：hub **分支 B** 写入执行体。
