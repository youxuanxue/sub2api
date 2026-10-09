---
title: Multimodal supply capability SSOT（多模态供应能力门）
status: approved
approved_by: "feng (2026-10-08 conversation: 同意薄切片 — ContentKinds∩known-negative 准入，非重写 Plan; 同意把能力门做完整 — 空池专用 400)"
approved_at: 2026-10-08
created: 2026-10-08
authors: [auto]
risk: high
revision_note: "v1.3: OAuth edge serial probe 2026-10-09 — OpenAI edge supply-wide input_video hard-negative + gpt-5.3-codex-spark image; anthropic edge no hard-neg."
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
   | `glm-5.3-flash` | video | NVIDIA Build | `At most 0 video(s)` |
   | `glm-4.5-air` / `glm-5.2` / `glm-5.3` | video + image | Volc Agent Plan | `Model only support text input` |
   | `glm-4.5-air` / `glm-5.2` / `glm-5.3` | video + image | china-edge relay（`api-*.tokenkey.dev` newapi stub） | 同上 |
   | `gpt-5` / `gpt-5-mini` / `gpt-5.1` / `gpt-5.2` | video | tokensea（`agent.tokensea.ai`） | `Invalid value: 'video_url'`（image 可） |
   | `deepseek-flash` / `deepseek-v4.1-flash` | video | Ali Token Plan | `does not support video input`（image 可；Volc 可视频） |
   | `qwen3.7-max` | video + image | Ali Token Plan | `Unexpected item type in content` |
   | `deepseek-v4-flash-0731` / `deepseek-v4-pro` / `deepseek-v4-pro-0813` / `glm-4.5-air` / `glm-4.7` | video + image | Qianfan Token Plan | `Invalid content type. *_url is only supported by certain models` |
   | *（供应级）* | video | OpenAI edge mirror（`api-*.tokenkey.dev` + platform=openai） | Responses `Invalid value: 'input_video'`（image 普遍可） |
   | `gpt-5.3-codex-spark` | image | OpenAI edge mirror | `does not support image input` |

   对照允许（不上负表）：`kimi-k3` video/image 在 Volc / china-edge；`glm-5.3-flash` video/image 在 Volc / china-edge；`glm-5.2`/`glm-5.3` video/image 在 Ali；`gpt-5.5+` video 在 tokensea；kimi-k3 image 在 NVIDIA；**Anthropic/Kiro edge** image+video 全系 gateway servable（无硬拒）；Ali 多数 `qwen*` video「does not meet the requirements」按素材问题处理。Gemini Web edge 探针组报 Unsupported model、Antigravity 多为 429 capacity——**不上** modality known-negative。
3. **准入**：`candidateSupportsRequest` 内与其它准入谓词求交（可在 Plan 前短路）；命中 known-negative → 该账号不是候选。
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
| OpenAI 多模态 conversion 是否保留 video | `protocolrouter/registry.go` → `preservesMessagesToResponsesContent` 允许集含 `ContentVideo` | 与 image 同级；不开放到 text-only Gemini 身份路径 |
| known-negative / admit 谓词 | `service/candidate_multimodal_supply_tk.go` | 唯一供应能力表；禁止在 handler 复制 |
| 准入接线 | `service/candidate_eligibility.go` → `candidateSupportsRequest` | 调用 admit 谓词；不在此写表 |
| 契约与扩展节奏 | 本文 | 扩表必须附探针证据日期与 URL/日志锚点 |

## 客户请求形态（TokenKey 实测，2026-10-08 prod 1.8.279）

不要写成「Kimi 官方要求走 Responses」。Moonshot 官方主路径是 Chat Completions + `video_url`（`ms://` 或 base64，**不支持公网 HTTP URL**）。TokenKey 当前供应是 Volc Agent Plan，下列形态已探针：

| 入口 | 形态 | 结果 |
| --- | --- | --- |
| `POST /v1/chat/completions` | `video_url.url` = `data:video/mp4;base64,...` | **servable**（account 88；识别橙色样本） |
| `POST /v1/chat/completions` | `video_url.url` = 供应侧可达 HTTPS（国内样本） | **servable**（account 88） |
| `POST /v1/chat/completions` | 境外公网 HTTPS（w3schools 样本） | **400** `invalid_request_error` |
| `POST /v1/responses` | `input_video` + 供应侧可达 HTTPS | **servable**（此前同日探针） |

Quickstart 客户示例 owner：`frontend/src/utils/kimiK3VideoExamples.tk.ts`（curl/python 附加块；密钥自检仍是文本 ping）。NVIDIA Build 对 `kimi-k3` 视频仍是 known-negative，不出现在客户「可用形态」表。

## 与 Plan 的分工

- `Router.Plan`：协议/converter **能否表达**该请求（含 ContentKinds）。
- 本文：在 Plan 合法的前提下，**这条供应是否已知能服务**该输入模态。
- 二者求交后才进入调度。计费组平台标签仍不参与选号。

## 空合格供应出口（v1.1）

当授权范围内**每一个**本来能映射该模型的供应都命中 known-negative（例：池里只有 NVIDIA Build 的 `kimi-k3` + video）时：

- 哨兵：`ErrUnsupportedInputModality`（消息**不含** `no available accounts`）
- 客户端：HTTP **400** `invalid_request_error`，文案对齐 OpenRouter：  
  `No available endpoints support input <modality> for model: <id>`（`<modality>` 为请求实际被拒的 `video` / `image`，不得硬编码成 video）
- **不是** capacity 429，也不是 `Unsupported model`（模型名合法，缺的是模态供应）
- 接线：
  - Universal：`candidateSupportsRequest` → `PrepareCandidateRequest`
  - Direct OpenAI/newapi：`openAIRequestEligibilityReason` → filter reason `input_modality_unsupported` → `openAIFilterOnlyReason` 空池升级（升级时用 `requestInputModalityPreference`）
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
| E2 | 仅 image known-negative 空池（如 china-edge `glm-5.3` + image） | 400 + message 含 `input image`，不得写成 `input video` |

自动化：`candidate_multimodal_supply_tk_test.go` + parse ContentVideo 单测 + selection-failure handler 单测。  
Sentinel：`scripts/sentinels/gateway-tk.json` 锚定 owner 调用。

## 后续

- audio known-negative 扩表；Cursor Agent 直连路径仍失败；Gemini Web / Antigravity 在探针组 mapping 与容量恢复后可再采 modality 硬证。
- 能力投影进 discovery / `/models` modalities（可选）。
- `/models` 或 capabilities 暴露 per-supply input modalities（可选，非选号正确性必需）。
