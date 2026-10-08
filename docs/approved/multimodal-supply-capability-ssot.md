---
title: Multimodal supply capability SSOT（多模态供应能力门）
status: approved
approved_by: "feng (2026-10-08 conversation: 同意薄切片 — ContentKinds∩known-negative 准入，非重写 Plan)"
approved_at: 2026-10-08
created: 2026-10-08
authors: [auto]
risk: high
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
2. **供应侧 known-negative 表**（证据驱动，禁止臆造全表）：
   - `kimi-k3` + input video + NVIDIA Build（`integrate.api.nvidia.com`）→ **硬排除**  
     证据：上游 `At most 0 video(s)`（2026-10-08 probe）。
   - `kimi-k3` + input video + Volc Agent Plan → **允许**（同日 probe servable）。
3. **准入**：`candidateSupportsRequest` 内与其它准入谓词求交（可在 Plan 前短路）；命中 known-negative → 该账号不是候选。
4. **未知供应**：不在表内 → **仍准入**（LiteLLM 保守策略），待探针升级为已知。不把 v1 做成 known-positive 全表。

### 不做什么（v1）

- 不重写 `protocolrouter` converter 注册表或调度打分。
- 不做 `model=auto` 换模型（用户钉死模型 ID）。
- 不把 NewAPI pre-output 换号当作多模态正确性方案。
- 不臆造全供应商 × 全模态矩阵；无证据不上表。
- 不在 v1 强制「空集时专用错误码」——若池中仍有合格供应则静默剔除负例；若授权范围内无合格供应，沿用现有 empty-pool / unsupported 出口（后续可加专用 400 文案）。

## Owners

| 事实 | Owner | 边界 |
| --- | --- | --- |
| 请求 ContentKinds（含 ContentVideo） | `protocolrouter/parse_request.go` → `protocolContentKinds` | 只解析入站 body；不读账号 |
| OpenAI 多模态 conversion 是否保留 video | `protocolrouter/registry.go` → `preservesMessagesToResponsesContent` 允许集含 `ContentVideo` | 与 image 同级；不开放到 text-only Gemini 身份路径 |
| known-negative / admit 谓词 | `service/candidate_multimodal_supply_tk.go` | 唯一供应能力表；禁止在 handler 复制 |
| 准入接线 | `service/candidate_eligibility.go` → `candidateSupportsRequest` | 调用 admit 谓词；不在此写表 |
| 契约与扩展节奏 | 本文 | 扩表必须附探针证据日期与 URL/日志锚点 |

## 与 Plan 的分工

- `Router.Plan`：协议/converter **能否表达**该请求（含 ContentKinds）。
- 本文：在 Plan 合法的前提下，**这条供应是否已知能服务**该输入模态。
- 二者求交后才进入调度。计费组平台标签仍不参与选号。

## 验收（v1）

| # | 场景 | 期望 |
| --- | --- | --- |
| P1 | `kimi-k3` + video，池含 Volc Agent Plan + NVIDIA Build | 只准入 Volc；最终可落到 Volc |
| N1 | `kimi-k3` + video，池仅 NVIDIA Build | 无候选 / 不发起 NVIDIA 上游视频调用 |
| R1 | `kimi-k3` 纯文本，池含 NVIDIA | NVIDIA 仍准入（不回归） |
| R2 | 未建表的账号/模型 + video | 仍准入（未知保守） |

自动化：`candidate_multimodal_supply_tk_test.go` + parse ContentVideo 单测。  
Sentinel：`scripts/sentinels/gateway-tk.json` 锚定 owner 调用。

## 后续（非 v1）

- image known-negative / known-positive 扩表（探针驱动）。
- 空合格供应时稳定的客户端错误文案（对齐 OpenRouter “no endpoints support input video” 语义）。
- 能力投影进 discovery / `/models` modalities（可选）。
