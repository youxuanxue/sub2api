---
name: tokenkey-servable-model-refresh
description: >-
  Refresh TokenKey public pricing and menu served-model allowlists from real upstream probes. Use when the catalog/menu is stale, models may no longer serve 200, or ops/pricing refresh/probe automation needs to update supported maps.
---

# TokenKey：实测可服务模型 allowlist 刷新

适用于本仓库（TokenKey fork of sub2api）。把「公开 `/pricing` 与用户 `Your Menu`
应该展示哪些实测可服务模型」从一次性手工探测固化为可复跑流水线。背景与解耦原因见
`ops/pricing/README.md`、PR #605（呈现层过滤 vs IsModelPriced 解耦）、#608（本工具）。

当前公开目录和用户菜单已收敛到同一 servable surface：

- `FilterPublicCatalogToServable` 过滤公开 `/pricing`。
- `supportedCatalogModelIDsForPlatform` 喂给用户菜单 fallback。
- 两者共享 `pricing_catalog_supported_models_tk.go` 中的经验可服务集合。

所以本 skill 只刷新这个共享集合；不要再为菜单维护第二份列表。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。本 skill
**已达基线**——可机械化的步骤全在脚本里，prompt 不重复它们：

- **机械化（脚本承载）**：候选派生（按 litellm vendor + 是否有价，分 chat/responses/image
  家族，Gemini 另走 discovered + seed）、SSM 投递与逐模型请求、HTTP→verdict 分类、留
  `servable`、dated 去重、Go map splice、分批避开 SSM 等待窗口、自动开 PR——全在
  `refresh-servable-allowlist.py` / `probe-servable-models.sh`，`selftest` 子命令覆盖，
  preflight `servable-allowlist generator selftest` 门禁 + sentinel 守 splice 标记。
- **真判断（留给人/agent）**：① `inconclusive`（429/502/503）的取舍——它常是「该探测组没有
  这类账号」而非模型本身不可用（如 image 经 GPT专线组、专用 codex 池）；要不要给别的组 key
  扩探测再加回，是判断。② 审 PR diff 是否合理（突然大幅增删要查是不是探测设置坏了，看
  `auth_error` 行）。③ 合并授权（人）。

## 用法

需运营本机有 AWS creds（探测走 prod SSM）。

```bash
# 0) 预览候选切分（无需 prod）
python3 ops/pricing/refresh-servable-allowlist.py candidates

# 1) 一键：探测 → 重写 Go allowlist → 自动提 PR
python3 ops/pricing/refresh-servable-allowlist.py run --open-pr

# 或分步，先看原始 verdict 再决定：
python3 ops/pricing/refresh-servable-allowlist.py probe | tee /tmp/servable.tsv
python3 ops/pricing/refresh-servable-allowlist.py apply --results /tmp/servable.tsv
cd backend && go test -tags=unit ./internal/service/ -run PublicCatalog
```

`run` 不带 `--open-pr` 只重写本地 Go 文件，便于先审 `git diff`。

## Gemini / Vertex 三族（newapi 第五平台，探测目标 us6）

claude/gpt 经 prod 探测；**gemini/Vertex 经 us6 的 `google` 组探测**（该组账号在 us6）。三族：
`GEMINI_CHAT_MODELS`→`/v1/chat/completions`、`GEMINI_IMAGE_MODELS`→`/v1/images/generations`、
`GEMINI_VIDEO_MODELS`→`/v1/video/generations`（异步 submit 200 即 servable，best-effort）。
probe key 取自**绑定 `google` 组的 api_key**（`api_keys.group_id→groups.id`，永不回显）。

**edge 内网访问（关键，否则全 403）**：edge 的 Caddy 把 `/v1/*` 只放行给 prod 网关 CIDR
（`Caddyfile.edge` 的 `@allowed_relay … remote_ip ${MAIN_GATEWAY_ALLOWED_CIDR}`），edge 主机本地直打
公网 `api-<edge>` 域名会被 Caddy 返 `edge relay path is restricted` 403。所以 gemini 三族**在 edge
主机上经 `docker exec <app容器> wget http://tokenkey:8080` 直打 app、绕过 Caddy**（`GEMINI_APP_CONTAINER`
/`GEMINI_APP_URL`）。这测的是同一条 account→Vertex 真实链路，Caddy 只是访问控制层、不影响模型可服务性。
（「对客 prod→edge relay 拓扑」是另一回事——是产品/架构决策，与本探测无关。）

- **候选来源（不走 litellm）**：账号的 `credentials.model_pricing_status`（上游发现清单）∪ imagen/veo
  种子。经 `--discovered <file>` 传入（接受该 JSON 对象、JSON list 或换行清单）；省略则只探 imagen/veo 种子。
  ```bash
  # 先从 us6 account 3 拉发现清单（只读），存成 JSON 再喂给候选
  # （model_pricing_status 是对象，键即模型名；工具取其 keys）
  python3 ops/pricing/refresh-servable-allowlist.py candidates --discovered /tmp/mps.json
  python3 ops/pricing/refresh-servable-allowlist.py run --discovered /tmp/mps.json   # 探测+重写
  ```
- **范围 = 仅核心生成族**（chat/image/video）。`GEMINI_EXCLUDE_RE` 排除 gemma/lyria/deep-research/
  robotics/antigravity/computer-use/tts —— 避免清空 mapping 后这些未定价冷门模型被静默 $0 服务。
- **gemini 集为空 = passthrough**：Go 里 `supportedGeminiCatalogModels` 空时公开目录/菜单不收窄
  （落脚手架零回归），探测填充后才激活闸门。

### 清空某 Vertex 账号 model_mapping（catch-all）前的安全闸

清空 `model_mapping` → 账号放行全部模型；**未定价模型会静默计 $0**（`pricing_missing_record_zero_cost`）。
务必按序，别跳：

1. **探测窗口**：临时清空该账号 mapping + `schedulable=true` → 经 us6 探测全核心候选 → **立即还原**。
   （us6 `google` 组当前无真实客流，窗口内基本只有探测自身请求。）
2. **对账 `servable ∩ unpriced`**：以探测窗口内的 `pricing_missing_record_zero_cost` 日志为真值
   （比 `model_pricing_status` 快照可靠），与发现清单对账。
3. **补准价（禁臆造）**：每个 servable-且-缺价的核心模型，查 **Google Vertex 官方实价**补
   `backend/internal/service/tk_pricing_overlay.json`，形状对齐既有 imagen/veo 条目并带真实 `source`
   （URL+抓取日）。**无公开价的模型必须排除出 catch-all**（或暂缓），不得估价。
4. 发版 → soak 回读确认零 $0 → **此时才**永久清空 `model_mapping` + `schedulable=true`
   （裸 SQL 后刷 `scheduler_outbox`，见 memory `gemini_media`）。

## Volcengine / ark 三族（直连数据面，开通真值，免调度窗口）

volcengine（newapi `channel_type=45`）模型的「有权限访问」检查**不走 TK 网关**，三族直打 ark 数据面：
`ARK_CHAT_MODELS`→`/api/v3/chat/completions`、`ARK_IMAGE_MODELS`→`/api/v3/images/generations`、
`ARK_VIDEO_MODELS`→`/api/v3/contents/generations/tasks`。凭据取自 `accounts.id=ARK_ACCOUNT_ID`
（默认 7）的 `credentials.api_key/base_url`，**账号保持 `schedulable=false` 也能探**——零 prod
配置改动、零客户暴露面。

```bash
bash ops/observability/run-probe.sh --target prod --script ops/pricing/probe-servable-models.sh \
  --env "ARK_CHAT_MODELS=doubao-seed-1-6-250615 kimi-k2-250711" --timeout-seconds 300
```

- **为什么直连**（2026-06-10 实证）：ark 的 `GET /api/v3/models` 是**平台目录非开通清单**（kimi/qwen
  在列但调用全拒）；经 TK 网关探测时 ark 的 404 被 bridge 包成不透明 502 读不出语义。直连后判别确定：
  **200=已开通；404 `InvalidEndpointOrModel.NotFound`=未开通/已下线（落 unsupported）；429/5xx=transient**。
- **费用**：未开通模型的 404 免费；已开通模型 chat 仅 16 token、image 计 ~1 张、**video 会创建真实
  付费任务**——video 族只对真要上架的模型探，别全量扫。
- **结果不进 Go allowlist**：`refresh-servable-allowlist.py parse_results` 只认 anthropic/openai/gemini，
  `volcengine` 行天然忽略（该 vendor 在公开目录是 passthrough）。本族是运营对账工具：输出与账号
  `model_mapping` 做差集 → 未开通的从 mapping 清掉，已开通∩未定价的先补价再放行（对照 deepseek-v4
  渠道定价样板，见 memory `volc_video_submit_200_and_ark_pricing_path`）。

## 判断要点 / 坑

- **verdict 语义**：200=servable（留）；400/404+retired/not-found/"not supported when using
  Codex"=unsupported；429/502/503=inconclusive（容量/协议/该组无账号）；401/403=auth_error
  （探测设置坏了，不是模型信号——先修 key/形状再重跑）。
- **探测覆盖面**：anthropic 仅经 **edge-us7**、openai 仅经 **GPT专线组**。只由别的组服务的模型
  在此读 inconclusive 被删；要保留得给那个组的 key 并扩 `probe-servable-models.sh`。
- **探测形状**（改 probe 时勿破坏，否则全假阴）：claude 路径要 `User-Agent: claude-cli/...`
  + `anthropic-beta: claude-code-20250219` + cc system + `metadata.user_id` 是**字符串**；
  codex 走 `/v1/responses`。详见 probe 脚本头注释。
- **改了 allowlist 后**：公开目录 + 我的菜单两面同源（见
  `supportedCatalogModelIDsForPlatform` / `FilterPublicCatalogToServable`），上线需**发版**才生效。
- **合并永远等人授权**；本 skill 的 `--open-pr` 只开 PR，不合并。

## 姊妹 runbook：缺价模型定价热更新

收到飞书「**模型缺价（已记零成本）**」卡片（PricingMissingNotifier：缺价模型**照常服务**、
按零成本记账，不拒绝客户）时，处置走 `ops/pricing/apply-pricing-hotfix.py`：

```bash
python3 ops/pricing/apply-pricing-hotfix.py lookup --model <模型名>   # litellm 全量源取价（含被裁剪镜像丢掉的带前缀键）
export TOKENKEY_ADMIN_API_KEY=...                                     # settings.admin_api_key
python3 ops/pricing/apply-pricing-hotfix.py channels                  # 选 --channel-id
python3 ops/pricing/apply-pricing-hotfix.py apply --model <模型名> --channel-id N --platform <平台> --from-litellm --yes   # 热更：渠道定价凌驾一切，立即生效无需发版
python3 ops/pricing/apply-pricing-hotfix.py stage-overlay --model <模型名> --from-litellm   # 固化：fill-only 进 tk_pricing_overlay.json，提 PR
```

细则（per-channel 语义、litellm 没收录时的 `--entry-json` 路径、镜像价格错误时只能用渠道定价修）
见 `ops/pricing/README.md` §"Pricing-missing hotfix"。

## 姊妹入口：modelops planner

当问题不是“刷新公开/菜单 allowlist”，而是“某个 newapi 长尾模型、生产 mapping、Qwen-2
备份、定价、probe 结果之间哪里漂了”，先跑：

```bash
python3 ops/pricing/modelops.py plan \
  --upstream 60:/tmp/qwen_upstream_models.json \
  --probe-results /tmp/qwen_probe.tsv \
  --live-mapping /tmp/qwen_mapping_snapshot.json \
  --mirror 60:72
```

`modelops.py` 只出计划，不写生产；真正刷新本 skill 管的 catalog/menu surface 仍用
`refresh-servable-allowlist.py`。
