---
name: tokenkey-servable-model-refresh
description: >-
  刷新 TokenKey 公开 /pricing 目录 + 「我的菜单」的「实测可服务模型」allowlist
  （candidates → probe → apply → PR）。经 prod SSM 逐平台逐模型发真实请求实测
  （anthropic 走 edge-us7 的 Claude-Code 形 /v1/messages；openai 走 GPT专线 key 的
  /v1/chat/completions，codex 走 /v1/responses，image 走 /v1/images 尽力探），**只保留
  返回真 200 的**，去重 dated 快照（anthropic -YYYYMMDD / openai -YYYY-MM-DD，丢
  dated-with-base + -thinking），splice 回 backend/internal/service/
  pricing_catalog_supported_models_tk.go 的两个 Go map 并自动提 PR。单一脚本
  ops/pricing/refresh-servable-allowlist.py 编排 + ops/pricing/probe-servable-models.sh
  探测；selftest + preflight 门禁 + sentinel 守护确定性与 splice 标记。canonical/
  广告状态无关——纯实测。Use when 刷新可服务模型清单、公开目录或我的菜单出现陈旧/
  不可用模型、实测某平台模型是否还能经 TokenKey served、或重跑 2026-06-05 那次手工探测。
---

# TokenKey：实测可服务模型 allowlist 刷新

适用于本仓库（TokenKey fork of sub2api）。把「哪些 claude/gpt 模型现在真能经
TokenKey served」从一次性手工探测固化为可复跑流水线。背景与解耦原因见
`ops/pricing/README.md`、PR #605（呈现层过滤 vs IsModelPriced 解耦）、#608（本工具）。

## 确定性基线（机械化 vs 真判断）

按 dev-rules `rules/dev-rules-convention.mdc` §「skill / command 确定性基线」自审。本 skill
**已达基线**——可机械化的步骤全在脚本里，prompt 不重复它们：

- **机械化（脚本承载）**：候选派生（按 litellm vendor + 是否有价，分 chat/responses/image
  家族）、SSM 投递与逐模型请求、HTTP→verdict 分类、留 `servable`、dated 去重、Go map
  splice、分批避开 SSM 等待窗口、自动开 PR——全在
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
