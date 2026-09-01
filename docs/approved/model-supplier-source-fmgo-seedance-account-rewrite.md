---
title: FMGo Seedance — account/relay SKU rewrite (not supplier runtime)
status: approved
approved_by: "xuejiao (operator directives 2026-09-01)"
approved_at: 2026-09-01
updated: 2026-09-01
created: 2026-09-01
owners: [tk-platform]
scope: "FMGo ch54: models are 2 official remaps; 16 SKUs live in notes; runtime rewrite on account video relay; capability set pinned in adaptor"
related_stories: ["US-048"]
related_design: ["docs/approved/model-supplier-source-probe-sync-split.md"]
amends: ["docs/approved/model-supplier-source-management.md"]
operator_locks: "2026-09-01: models exactly 2 official remaps; 16 SKUs notes-only not projected; capability set in FMGo adaptor; missing res/dur→max; illegal→reject; no supplier_source_id in gateway"
---

# FMGo Seedance：账号侧动态改写（审批草案）

## 一件事

客户只打 2 个官方 Seedance 2.0 client id。线上由**账号视频通道**按请求参数改写为 FMGo SKU。

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

`{480p,720p} × {6,8,10,12,15}` **钉在 FMGo 账号通道 adaptor 里**。
不读供应源表，不从 notes 解析，不读账号 Extra 里的供应源字段。

上游方言（飞秒使用指南）：`feimiao-v2` / `feimiao-v2-fast` 走
`https://api.fmgo.top` 的 `POST /v1/chat/completions`（`Prefer: respond-async`、`async: true`、`generationConfig.videoConfig`），轮询 `GET /v1/tasks/{task_id}`。
不是 `/v1/video/generations`，也不是通用 Chat 伪成功。

## 已拍板

| 项 | 决定 |
|---|---|
| 对外 client | 仅 `doubao-seedance-2-0-260128`、`doubao-seedance-2-0-fast-260128` |
| 供应源 `models` | **恰好 2 行官方 remap**，禁止再存 16 行 identity |
| 16 条 `feimiao-v2-*` | 只写在 `notes`（采购备忘），**不进 models、不进账号 mapping、不进对外目录** |
| 同步投影 | 投影这 2 行。不同步为 16 个 client。同步不靠「聪明过滤」 |
| 探测 | `channel_type=54` → 视频门禁；只探这 2 行的锚点 upstream |
| 非法 resolution / duration | **拒绝**，不降级、不钳制 |
| 缺省 resolution | `720p`（集合最大） |
| 缺省 duration | `15`（集合最大秒） |
| 运行时 SKU | 按参数动态拼；mapping 的 upstream 只是探测锚点，不是唯一永久上游 id |

不设 `projectable` / `inventory_only` 标记。不设静态 fallback SKU。

现网若仍是 16 行 identity：本实现 PR 先把该供应源收成 2 行再同步。

## 上游能力集合（adaptor 内钉死）

```text
host       ∈ {api.fmgo.top, www.fmgo.top, fmgo.top} → canonical https://api.fmgo.top
resolution ∈ {480p, 720p}
duration   ∈ {6, 8, 10, 12, 15}
SKU        = feimiao-v2[-fast]-{resolution}-{duration}s
submit     = POST /v1/chat/completions
poll       = GET /v1/tasks/{id}
```

## 供应源唯一存法

| client_model_id | upstream_model_id（探测锚点） | 同步进账号 mapping |
|---|---|---|
| `doubao-seedance-2-0-260128` | `feimiao-v2-720p-15s` | 是 |
| `doubao-seedance-2-0-fast-260128` | `feimiao-v2-fast-720p-15s` | 是 |

`notes` 可列出 16 个 SKU 备忘。运行时不读 notes。

## 运行时改写（账号 / video relay）

账号 mapping 表明本账号可承接该官方 client 后：

1. fast 与否 → `feimiao-v2-fast-*` / `feimiao-v2-*`
2. resolution：缺省 → `720p`；∈ 集合 → 用其值；否则明确拒绝
3. duration：缺省 → `15`；∈ 集合 → 用其值；否则明确拒绝
4. 拼出唯一上游 SKU，按官方 chat-completions 视频方言提交

## 探测

- `channel_type=54` → 与账号通道同一上游方言。
- FMGo：官方 `POST /v1/chat/completions` 视频体（不是「hi」Chat 伪成功）。
- 只探两行锚点：`feimiao-v2-720p-15s`、`feimiao-v2-fast-720p-15s`。
- 失败不写账号。禁止用普通 Chat Completions 探测冒充视频可服务。

## Scenarios

### 正向

1. 同步后账号 mapping 仅含 2 个官方 client。
2. `…-260128` + `720p` + `10` → `feimiao-v2-720p-10s`。
3. 省略 resolution/duration → `720p` + `15s` → `feimiao-v2-720p-15s`（或 fast 变体）。

### 负向

1. `1080p` / `4k` / `9s` → 拒绝。
2. 保存或同步把 `feimiao-v2-*` 写成 client → 缺陷；测试锁死不出现。
3. 网关不查供应源表、不解析 notes。

## Validation

- 单测：缺省 → 最大；非法 → 拒绝；fast/非 fast 拼 SKU。
- 单测：同步投影后 mapping 客户端集合 == 2 个官方 id。
- 单测：adaptor 能力集合不从供应源/notes 读取。
- 上游视频 task 404 时探测失败，不写账号。

## 非目标

- 不把 16 个 `feimiao-v2-*` 进 models / 目录 / 对外 client。
- 不映射 2.5 / mini / 1.x。
- 不在供应源服务内拼运行时 SKU。
- 不实现探测/同步按钮拆分。
- 不做 inventory 行标记。

## 审批检查清单

- [x] models 恰好 2 行；16 条只在 notes
- [x] 能力集合钉在 adaptor
- [x] 缺省取集合最大；非法拒绝
- [x] 改写在账号 relay；不读供应源
- [x] 已批准（2026-09-01）
