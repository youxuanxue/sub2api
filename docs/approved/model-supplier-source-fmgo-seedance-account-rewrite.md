---
title: FMGo Seedance — account/relay SKU rewrite (not supplier runtime)
status: approved
approved_by: "xuejiao (operator directives 2026-09-01; live-group override 2026-09-01)"
approved_at: 2026-09-01
updated: 2026-09-01
created: 2026-09-01
owners: [tk-platform]
scope: "FMGo ch54: models are 2 official remaps onto live 431 inventory; runtime rewrite on account video relay; capability set pinned in adaptor"
related_stories: ["US-048"]
related_design: ["docs/approved/model-supplier-source-probe-sync-split.md"]
amends: ["docs/approved/model-supplier-source-management.md"]
operator_locks: "2026-09-01 live default group: official Seedance remaps to 431[-fast]; submit POST /v1/videos; v2.5/mini dialect supported as passthrough; capability set in FMGo adaptor; missing res/dur→family default; illegal→reject; no supplier_source_id in gateway"
---

# FMGo Seedance：账号侧动态改写（审批草案）

## 一件事

客户只打 2 个官方 Seedance 2.0 client id。线上由**账号视频通道**按请求参数改写为飞秒现网目录里已有的 SKU。

供应源不进入 scheduler / gateway。探测/同步按钮见  
`docs/approved/model-supplier-source-probe-sync-split.md`（可并行，非实现前置）。

本文件已批准，实现按本文执行。

## 硬边界

```text
调度 / 网关  ←只看→  accounts
供应源        ←只做→  采购事实 + 同步时投影账号字段
```

识别 FMGo dialect：账号上的 `channel_type` + `base_url`（类比 XRToken ch54）。  
**禁止**读 `supplier_source_id`。

能力集合**钉在 FMGo 账号通道 adaptor 里**。
不读供应源表，不从 notes 解析，不读账号 Extra 里的供应源字段。

现网 default 组目录里没有 `feimiao-v2[-fast]-*`。Seedance 2.0 对应
`feimiao-v2-431[-fast]-*`（上游回显 `seedance-2.0` / `seedance-2.0-fast`）。
同组还有 `feimiao-v2.5-*`、`feimiao-v2-mini-*`，走同一套 `/v1/videos` 方言。

| 族 | 提交 | 轮询 |
|---|---|---|
| 官方 Seedance / 431 / v2.5 / mini | `POST /v1/videos` | `GET /v1/videos/{id}`（完成字段 `result_url`） |
| legacy `feimiao-v2[-fast]` | `POST /v1/chat/completions` + `Prefer: respond-async` + `async:true` + `generationConfig.videoConfig` | `GET /v1/tasks/{id}` |

都不是 `/v1/video/generations`，也不是通用 Chat 伪成功。

## 已拍板

| 项 | 决定 |
|---|---|
| 对外 client | 仅 `doubao-seedance-2-0-260128`、`doubao-seedance-2-0-fast-260128`（Studio / 目录不暴露飞秒 SKU） |
| 探测 | 列出 `/v1/models` 里**全部可探视频库存**（v2.5 / 431 / mini 网格，以及 veo / grok-video / omni 等）；跳过 Claude/GPT。结果应是很多行，不是 2 行 |
| 供应源 `models` | 探测通过的建议追加是 opt-in。若只要承接官方 Seedance，保存 2 行 remap；若要把库存写进采购表，可加入全部通过的 identity 行 |
| 同步投影 | 投影**当时已保存的** `models` 行。探测到很多 SKU ≠ 自动同步很多 client |
| 非法 resolution / duration | **拒绝**，不降级、不钳制 |
| 缺省 resolution | `720p` |
| 缺省 duration | `15`（mini 720p 例外为 `10`） |
| 运行时 SKU | 按参数动态拼；mapping 的 upstream 只是探测锚点，不是唯一永久上游 id |

不设 `projectable` / `inventory_only` 标记。不设静态 fallback SKU。

1.8.193 只会走 chat completions，**发新版后才能探测/同步 videos 族**。

## 上游能力集合（adaptor 内钉死）

```text
host       ∈ {api.fmgo.top, www.fmgo.top, fmgo.top} → canonical https://api.fmgo.top
official   → feimiao-v2-431[-fast]-{resolution}-{duration}s
resolution ∈ {480p, 720p}
431 dur    ∈ {10, 15}
v2.5       透传目录组合（480p: 5/10/15/30；720p: 10/15/30）
mini       仅 (720p,10) / (480p,15)
legacy v2  {480p,720p} × {6,8,10,12,15}，仍走 chat completions
```

## 供应源唯一存法

| client_model_id | upstream_model_id（探测锚点） | 同步进账号 mapping |
|---|---|---|
| `doubao-seedance-2-0-260128` | `feimiao-v2-431-720p-15s` | 是 |
| `doubao-seedance-2-0-fast-260128` | `feimiao-v2-431-fast-720p-15s` | 是 |

`notes` 可列出 v2.5 / 431 / mini 备忘。运行时不读 notes。

## 运行时改写（账号 / video relay）

账号 mapping 表明本账号可承接该官方 client 后：

1. fast 与否 → `feimiao-v2-431-fast-*` / `feimiao-v2-431-*`
2. resolution：缺省 → `720p`；∈ 集合 → 用其值；否则明确拒绝
3. duration：缺省 → `15`；∈ `{10,15}` → 用其值；`6/8/12` **拒绝**（相对旧 chat 族是行为变化）
4. 拼出唯一上游 SKU，按 `/v1/videos` 提交（`seconds` 为字符串）

已经是 `feimiao-v2.5-*` / `feimiao-v2-mini-*` / legacy `feimiao-v2-*` 的请求按族透传，不改写成官方 client。

## 探测

- `channel_type=54` → 与账号通道同一上游方言。
- 现网族：官方 `POST /v1/videos`（不是「hi」Chat 伪成功）。
- legacy `feimiao-v2[-fast]`：官方 `POST /v1/chat/completions` 视频体。
- **已配置行 + 全部未覆盖的视频候选都探**。default 组目录应出现 v2.5 / 431 / mini 多条 SKU（建议追加），不是只探两个锚点。
- 目录混有 Claude/GPT 时，候选探测跳过非视频库存。
- 失败不写账号。禁止用普通 Chat Completions 探测冒充视频可服务。
- 「加入表单」才会把建议追加写进 `models`；探测本身不改供应源、不同步账号。

## Scenarios

### 正向

1. 同步后账号 mapping 仅含 2 个官方 client。
2. `…-260128` + `720p` + `10` → `feimiao-v2-431-720p-10s`。
3. 省略 resolution/duration → `720p` + `15s` → `feimiao-v2-431-720p-15s`（或 fast 变体）。

### 负向

1. `1080p` / `4k` / `9s` / `6s` / `8s` / `12s` → 拒绝。
2. 保存或同步把 `feimiao-*` 写成 client → 缺陷；测试锁死不出现。
3. 网关不查供应源表、不解析 notes。
4. 探测候选不含 `claude-*` / `gpt-*`。

## Validation

- 单测：缺省 → 431 最大；非法（含旧 6/8/12）→ 拒绝；fast/非 fast 拼 SKU。
- 单测：同步投影后 mapping 客户端集合 == 2 个官方 id，锚点为 431。
- 单测：adaptor 能力集合不从供应源/notes 读取。
- 单测：live 族打 `/v1/videos`；legacy 族打 chat completions。
- 上游视频 task 404 时探测失败，不写账号。

## 非目标

- 不把飞秒目录 SKU 进 models / 目录 / 对外 client。
- 不把 v2.5 / mini 做成 TokenKey 对外 client（adaptor 只支持其 `/v1/videos` 方言与透传）。
- 不在供应源服务内拼运行时 SKU。
- 不实现探测/同步按钮拆分。
- 不做 inventory 行标记。

## 审批检查清单

- [x] 探测覆盖全部视频库存；官方 Seedance 对外仍是 2 个 client
- [x] 能力集合钉在 adaptor
- [x] 缺省取族默认；非法拒绝
- [x] 改写在账号 relay；不读供应源
- [x] 已批准（2026-09-01）；现网目录覆盖 431 / v2.5 / mini
