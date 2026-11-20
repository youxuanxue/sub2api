---
title: Multimodal supply capability SSOT（多模态供应能力门）
status: approved
approved_by: "feng (2026-10-08 薄切片 + 空池 400；后续扩表见 revision_note)"
approved_at: 2026-10-08
created: 2026-10-08
authors: [auto]
risk: high
revision_note: "v1.8 /v1/responses 视频显式 400；v1.7 modality 先于 Plan；v1.6 DashScope 视频；v1.5 Volc audio；v1.4 Ali glm-5.3 image。2026-11-20 删私有探针散文。"
---

# Multimodal supply capability SSOT

> 协作检索名：`multimodal-supply-capability-ssot`。  
> 父契约：[候选资格](candidate-eligibility-ssot.md)、[协议路由](protocol-routing-ssot.md)。  
> 本文只拥有「请求输入模态 × 供应侧已知能力」的准入交集；不拥有协议 converter 矩阵或调度权重。

## 产品契约

同一客户端 `model_id` 背后可以挂多条异构供应（例：`kimi-k3` 在 Volc Agent Plan 与 NVIDIA Build）。  
**同名不等于同能力。** 带视频输入时，不得选中**证据证明不能服务**该模态的供应；未知供应保守仍准入。命中 known-negative 的账号在选号前剔除，避免先打到已知不支持的上游再暴露晦涩 400。

## 薄切片范围（v1）

### 做什么

1. **请求侧**：`protocolrouter.ParseCanonicalRequest` 将 `video_url` / `input_video` 归类为 `ContentVideo`（与既有 text/image/audio/file 并列）。
2. **供应侧 known-negative 表**（证据驱动，禁止臆造全表；2026-10-08 prod 全供应×模型矩阵探针）：

   | 模型 | 模态 | 供应指纹 | 证据摘要 |
   | --- | --- | --- | --- |
   | `kimi-k3` | video | NVIDIA Build | `At most 0 video(s)` |
   | `kimi-k3` | video | DashScope 按量（`dashscope.aliyuncs.com`） | 2026-10-23 cn-prod #135：`Video inputs are not supported by this model`；image 可 |
   | `deepseek-v4.1-flash` | video | DashScope 按量（`dashscope.aliyuncs.com`） | 2026-10-23 cn-prod #135：`The current model does not support video input`；image 可；不含 `deepseek-flash` 别名 |
   | `kimi-k3` | audio | Volc Agent Plan | 2026-10-16 cn-prod 两账号返回 `audio input is not supported by this model` |
   | `glm-5.3-flash` | video | NVIDIA Build | `At most 0 video(s)` |
   | `glm-4.5-air` / `glm-5.2` / `glm-5.3` | video + image | Volc Agent Plan | `Model only support text input` |
   | `gpt-5` / `gpt-5-mini` / `gpt-5.1` / `gpt-5.2` | video | tokensea（`agent.tokensea.ai`） | `Invalid value: 'video_url'`（image 可） |
   | `deepseek-flash` / `deepseek-v4.1-flash` | video | Ali Token Plan | `does not support video input`（image 可；Volc 可视频） |
   | `glm-5.3` | image | Ali Token Plan | 2026-10-15：Responses 明确拒绝图片；Chat 200 但未识图 |
   | `qwen3.7-max` | video + image | Ali Token Plan | `Unexpected item type in content` |
   | `deepseek-v4-flash-0731` / `deepseek-v4-pro` / `deepseek-v4-pro-0813` / `glm-4.5-air` / `glm-4.7` | video + image | Qianfan Token Plan | `Invalid content type. *_url is only supported by certain models` |
   | *（供应级）* | video | OpenAI OAuth / SetupToken | Responses `Invalid value: 'input_video'`（image 普遍可） |
   | `gpt-5.3-codex-spark` | image | OpenAI OAuth / SetupToken | `does not support image input` |

   对照允许（不上负表）：`kimi-k3` video/image 在 Volc；`kimi-k3` image 与 `deepseek-v4.1-flash` image 在 DashScope 按量；`glm-5.3-flash` video/image 在 Volc；`glm-5.2` video/image 在 Ali（历史记录）；`glm-5.3` video 在 Ali 未因本次图片证据改变策略，旧 image 支持结论已撤销；`gpt-5.5+` video 在 tokensea；kimi-k3 image 在 NVIDIA；**Anthropic/Kiro** image+video 全系 gateway servable（无硬拒）；Ali 多数 `qwen*` video「does not meet the requirements」按素材问题处理。2026-10-23 DashScope 按量上 `deepseek-v4-pro` / `glm-5.3` 返回 200 但未看见图片或视频，`glm-5.3-flash` 为 `access_denied`，都不上表。Gemini Web edge 探针组报 Unsupported model、Antigravity 多为 429 capacity——**不上** modality known-negative。
3. **准入**：`candidateSupportsRequest` 与 Direct OpenAI `openAIRequestEligibilityReason` 均在 `protocolrouter.Plan` **之前**求交 modality 门；命中 known-negative → 该账号不是候选。顺序固定，避免 Chat-only 供应上音频/不可转换形状先变成 `no_legal_protocol_route`。
4. **未知供应**：不在表内 → **仍准入**（LiteLLM 保守策略），待探针升级为已知。不把 v1 做成 known-positive 全表。

### 不做什么（v1 / v1.1 / v1.2 / v1.3）

- 不重写 `protocolrouter` converter 注册表或调度打分。
- 不做 `model=auto` 换模型（用户钉死模型 ID）。
- 不把 NewAPI pre-output 换号当作多模态正确性方案。
- 不臆造全供应商 × 全模态矩阵；无硬负例证据不上表（opaque `Invalid request` / 429 / `model_not_found` / URL 拉不到都不上表）。
- 不把 URL 可达性（境外 HTTPS 拉不到）当成供应 known-negative——那是请求素材问题，不是选号问题。

## Owners

| 事实 | Owner | 边界 |
| --- | --- | --- |
| 请求 ContentKinds（含 ContentVideo） | `protocolrouter/parse_request.go` → `protocolContentKinds` | 只解析入站 body；不读账号 |
| OpenAI 多模态 conversion 是否保留 video | Chat/Messages↔Responses：`preservesMessagesToResponsesContent` 允许 `ContentVideo`；Responses→Chat：`preservesResponsesConversion` **不含** video（ingress + converter 显式 400） | Chat 视频仍可经 Messages↔Responses；`/v1/responses` 入站视频 fail-closed |
| known-negative / admit 谓词 | `service/candidate_multimodal_supply_tk.go` | 唯一供应能力表；禁止在 handler 复制 |
| 准入接线 | `candidateSupportsRequest`（Universal）与 `openAIRequestEligibilityReason`（Direct OpenAI/newapi）；均在 Plan **之前**调用 `accountInputModalityRejection` | 调用 admit 谓词；不在此写表。Chat-only 供应与 Responses→Chat preserve 见 [protocol-routing-ssot](protocol-routing-ssot.md) |
| 契约与扩展节奏 | 本文 | 扩表必须附探针证据日期与 URL/日志锚点 |

## 客户请求形态（契约）

Moonshot 官方主路径是 Chat Completions + `video_url`（`ms://` 或 base64；公网 HTTP 非官方主路径）。
TokenKey 当前 Volc Agent Plan 供应下：

| 入口 | 形态 | 结果 |
| --- | --- | --- |
| `POST /v1/chat/completions` | 可达 `video_url`（`data:video/*` base64 或供应侧可达 HTTPS） | servable |
| `POST /v1/chat/completions` | 本机预检不可达的 http(s) `video_url` | 本地 **400** `messages.content` |
| `POST /v1/responses` | `input_video` / `video` / `video_url` | **400** 显式拒绝（不把 silent-200 记为 servable） |

Quickstart 示例 owner：`frontend/src/utils/kimiK3VideoExamples.tk.ts`。NVIDIA Build × `kimi-k3` video 仍 known-negative。
http(s) 预检与 data: 门见 [`volc-kimi-chat-compatibility.md`](volc-kimi-chat-compatibility.md)。

一次性账号/Request ID/私有 `~/.local/share` 探针回执已删；扩表仍须附日期与可复核锚点，验收以
`candidate_multimodal_supply_tk_test.go` 及空池 handler 单测为准。

## 与 Plan 的分工

- `Router.Plan`：协议/converter **能否表达**该请求（含 ContentKinds）。
- 本文：在 Plan 合法的前提下，**这条供应是否已知能服务**该输入模态。
- 二者求交后才进入调度。计费组平台标签仍不参与选号。

## 空合格供应出口（v1.1）

当授权范围内**每一个**本来能映射该模型的供应都命中 known-negative（例：池里只有 NVIDIA Build 的 `kimi-k3` + video）时：

- 哨兵：`ErrUnsupportedInputModality`（消息**不含** `no available accounts`）
- 客户端：HTTP **400** `invalid_request_error`，文案对齐 OpenRouter：  
  `No available endpoints support input <modality> for model: <id>`（`<modality>` 为请求实际被拒的 `video` / `image` / `audio`，不得硬编码成 video）
- **不是** capacity 429，也不是 `Unsupported model`（模型名合法，缺的是模态供应）
- 接线：
  - Universal：`candidateSupportsRequest` → `PrepareCandidateRequest`
  - Direct OpenAI/newapi：`openAIRequestEligibilityReason` → filter reason `input_modality_unsupported` → `openAIFilterOnlyReason` 空池升级（升级时优先使用实际候选拒绝的模态）
  - Handler / Universal middleware：从 wrapped error 解析 modality 再调 `TkUnsupportedInputModalityMessage`

混有冷却/容量等原因时仍走既有 empty-pool 429；有合格供应时静默剔除负例。

## 验收（v1 + v1.1）

| # | 场景 | 期望 |
| --- | --- | --- |
| P1 | `kimi-k3` + video，池含 Volc Agent Plan + NVIDIA Build | 只准入 Volc；最终可落到 Volc |
| N1 | `kimi-k3` + video，池仅 NVIDIA Build | **400** `ErrUnsupportedInputModality`；不发起 NVIDIA 上游视频调用 |
| R1 | `kimi-k3` 纯文本，池含 NVIDIA | NVIDIA 仍准入（不回归） |
| R2 | 未建表的账号/模型 + video | 仍准入（未知保守） |
| E1 | N1 的客户端 envelope | 400 + `No available endpoints support input video for model: kimi-k3`，无 Retry-After |
| E2 | 仅 image known-negative 空池（如 Volc `glm-5.3` + image） | 400 + message 含 `input image`，不得写成 `input video` |

自动化：`candidate_multimodal_supply_tk_test.go` + parse ContentVideo 单测 + selection-failure handler 单测。  
Sentinel：`scripts/sentinels/gateway-tk.json` 锚定 owner 调用。

## 后续

- 其它供应/模型的 audio known-negative 按证据扩表；已退役供应不参与当前能力判定；Gemini Web / Antigravity 在探针组 mapping 与容量恢复后可再采 modality 硬证。
- 能力投影进 discovery / `/models` modalities（可选）。
- `/models` 或 capabilities 暴露 per-supply input modalities（可选，非选号正确性必需）。
