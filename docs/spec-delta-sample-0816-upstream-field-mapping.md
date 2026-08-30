# draft: sample-0816 字段对照 — 上游真实返回 vs TokenKey 日志 vs 样例导出

> **Status**: WIP 草稿（`docs/spec-delta-sample-0816-upstream-field-mapping.md`），供明日评审。未绑定实现 PR；不含 traj v2.1 落地设计细节。
>
> **样例路径**: `/Users/feng/Codes/data/sample-0816/`（两个包，解压后 290 条 session JSONL）
>
> **TokenKey 对照面**: QA 捕获 blob（`backend/internal/observability/qa/service.go` → `buildBlob`）、traj v2 投影（`backend/internal/observability/trajectory/projection_v2.go`）

## Background

外部交付的 `demo-v2-session` 样例（seed 20260805 / 0808-signature-50）用于 trajectory / thinking-signature 研究对齐。TokenKey 侧真实日志是 **逐 HTTP 调用** 的 QA blob（client-facing tee），traj v2 再投影为 session/turns。

本稿回答：**哪些字段是上游模型/API 真实返回的？哪些是样例 export pipeline 合成或从已有字段转换出来的？** 便于明日决定是否在 TokenKey 增加「样例对齐视图」（如 traj v2-sample 投影），或仅作外部格式文档。

### 样例顶层结构（data_intro 声明）

每条 JSONL = 一个 session，仅含：

| 字段 | 290/290 |
|------|---------|
| `messages` | ✓ |
| `tools` | ✓ |
| `model` | ✓ |
| `stop_reason` | ✓ |
| `format_kind` | ✓ |

`format_kind` 分布：`anthropic_messages` 174 · `openai_responses` 113 · `openai_chat` 3。

---

## 1. 上游 / TokenKey blob 中「真实存在」的字段

### 1.1 Anthropic `/v1/messages`（Opus / Fable 等 OAuth passthrough）

**单次 `response.body`（非流式）**

| 字段 | 说明 |
|------|------|
| `id`, `type`, `role`, `model` | 消息元数据 |
| `stop_reason`, `stop_sequence?` | 终止原因 |
| `usage` | `input_tokens`, `output_tokens`, `cache_*` 等 |
| `content[]` | 内容块数组 |

**`content[]` 块类型（上游原生）**

| `type` | 子字段 |
|--------|--------|
| `thinking` | `thinking`, `signature` |
| `redacted_thinking` | `data` |
| `text` | `text` |
| `tool_use` | `id`, `name`, `input` |

**流式 SSE**：`message_start` / `content_block_start|delta|stop` / `message_delta` / `message_stop`（含 `thinking_delta`, `signature_delta`, `text_delta`, `input_json_delta`）。

**`request.body`（历史）**

| 字段 | 说明 |
|------|------|
| `messages[].role`, `content` | user/assistant；content 可为 string 或 block 数组 |
| `tool_result` 块 | 在 **user** 消息内：`type`, `tool_use_id`, `content`, `is_error?` |
| `tools[]` | `name`, `description`, `input_schema`（非 OpenAI `function` 包装） |
| `system`, `thinking`, `model`, `temperature`… | 请求参数 |

TokenKey traj 单测中的 upstream 形态：thinking 在 `content[]` 内，**不在** `reasoning_details`：

```json
{
  "id": "msg_01",
  "model": "claude-opus-4-6",
  "stop_reason": "tool_use",
  "usage": { "input_tokens": 49, "output_tokens": 89 },
  "content": [
    { "type": "thinking", "thinking": "let me look", "signature": "REAL_SIG_123" },
    { "type": "text", "text": "Looking at the project." },
    { "type": "tool_use", "id": "tu_1", "name": "Bash", "input": { "command": "ls" } }
  ]
}
```

### 1.2 OpenAI `/v1/responses`（GPT-5.x upstream）

| 字段 | 说明 |
|------|------|
| `output[]` | `type`: `message` / `function_call` / `reasoning` 等 |
| `reasoning` item | `encrypted_content`（加密链）；可见时还有 summary/text |
| `usage`, `status`, `id`, `model` | 响应级 |

### 1.3 TokenKey QA blob 信封（非上游，但是「真实日志」层）

| 字段 | 说明 |
|------|------|
| `request_id`, `trajectory_id`, `captured_at` | 捕获元数据 |
| `request.path`, `request.body` | 客户端 inbound |
| `request.upstream_body`, `request.upstream_divergent` | 网关改写 + opt-in 时出现 |
| `response.status_code`, `headers`, `body` | 客户端 outbound（tee） |
| `stream.chunks[].t`, `raw_b64` | SSE 原始块 |
| `response.internal_thinking_blocks` | 网关 stash（Kiro/Gemini 等），**非** Anthropic 上游 |
| `redactions` | 脱敏标记 |

**注意**：`response.body` 是经网关 tee 的 **client-facing** 响应；Anthropic passthrough 时接近 upstream wire，经 OpenAI-compat 桥接时会变形。

---

## 2. 样例「多余」字段 — 合成（export 新建，上游无同名结构）

TokenKey 代码库中 **无** `reasoning_details` / `format_kind` / `anthropic_block_order` 等符号（全仓库 grep 为零，除 apicompat 测试 fixture 中的 `reasoning_details` 为 Chat 兼容路径）。

### 2.1 Session 级

| 字段 | 性质 |
|------|------|
| `format_kind` | 导出 wire 标签 |
| `model`（session 顶） | 多轮 **聚合** 展示模型 |
| `stop_reason`（session 顶） | 末轮 stop/finish **聚合** |

### 2.2 Message 级 — 统一 reasoning 载体

| 字段 | 性质 | 样例统计（37330 msgs） |
|------|------|-------------------------|
| `reasoning_details[]` | **整数组为合成**；OpenRouter 风格统一 reasoning | 9426 条 assistant |
| └ `format` | `anthropic-claude-v1` / `openai-responses-v1` | 合成枚举 |
| └ `type` | `reasoning.text` / `reasoning.encrypted` | 合成枚举 |
| └ `index` | 块序号 | 合成 |
| └ `responses_item_ref` | GPT Responses item 交叉引用 | 合成 |
| └ `id` | GPT reasoning item id 投影 | 合成 |
| `reasoning_content` | 多为 `reasoning_details[].text` 拼接；约 4464 条与 rd 不完全一致 | 7626 条 |
| `anthropic_block_order` | 如 `['thinking','text','tool_use']`，从 block 顺序 **推导** | 7931 条 |
| `anthropic_content_kind` | 恒为 `"blocks"` | 5872 条 |
| `anthropic_extras` | `{is_error}`, `cache_control` 等 **包装层** | 3540 条 |
| `phase` | GPT：`commentary` / `final_answer` | 2167 条 |
| `content[].responses_type` | Responses → chat-like 投影 | openai_responses 专用 |
| `tools[].responses_provider` | 工具 schema 导出元数据 | 1939 条 |
| `tools[].anthropic_provider` | 同上 | 4 条 |
| `<MASKED_*>` | 0808 包 de-identification | 合成 |

### 2.3 关键观察：thinking 不在 assistant `content[]`

Anthropic 样例中 assistant 的 `content[]` **从未**出现 `type=thinking`（统计 0 条）；thinking/signature 全部在 `reasoning_details`。这不是 upstream wire 形态，是 **exporter 投影策略**。

0808 包 reasoning 形态（anthropic-claude-v1）：sig-only 3421 · sig+text 3269。

---

## 3. 样例「多余」字段 — 转换 / 归一化（有来源，形状变了）

| 样例呈现 | 真实来源 | 转换说明 |
|----------|----------|----------|
| `messages[]` 整段对话 | 多轮 QA `request.body.messages` + 各轮 `response.body` | 前缀递增拼接 |
| `tools[]` | 某次 `request.tools` | 快照；常包成 `{type, function}` |
| `assistant.tool_calls[]` | `content[].tool_use` 或 Responses `function_call` | **12450 条全是 tool_calls**；content 内无 `tool_use` |
| `role=tool` + `tool_call_id` | Anthropic `user` 内 `tool_result` | 原生是 **user+tool_result**，非 OpenAI `role:tool` |
| `assistant.content[]`（text/image） | response text/image 块 | **剥离** thinking / tool_use |
| `reasoning_details[].signature` | `content[].thinking.signature` 或 SSE `signature_delta` | 从 thinking 块 **抽出** |
| `reasoning_details[].text` | `content[].thinking.thinking` 或可见 summary | 并列存放 |
| `reasoning_details[].encrypted_content` | Responses `output[].reasoning.encrypted_content` | GPT 加密链 |
| `tool` 消息 `is_error` | `tool_result.is_error` | 字段保留，message 形状变了 |
| `tool_calls[].anthropic_extras` | `tool_use` 附加属性 | 5883 条 |
| GPT `stop_reason=stop` | Responses finish 语义 | 映射为 chat 风格 |

---

## 4. TokenKey 有、样例弱化或缺失

| TokenKey / upstream | 样例 |
|---------------------|------|
| `content[].type=thinking` + `signature`（Anthropic 原生位置） | 0 条在 content；全在 `reasoning_details` |
| 每轮 `response.id`, `usage` | session 级无 per-turn usage |
| `request.system`, `request.thinking` | session 未保留（traj v2 `meta` 才有） |
| `stream.chunks` 原始 SSE | 无 |
| `internal_thinking_blocks` | 无 |
| `request.upstream_divergent` | 无 |
| traj v2 `blocks[]` + `call_meta.thinking_source/effort` | 样例用 `reasoning_details` + `anthropic_block_order` |

---

## 5. 按 `format_kind` 对照

| 维度 | `anthropic_messages` | `openai_responses` | TokenKey QA（Anthropic passthrough） |
|------|----------------------|--------------------|--------------------------------------|
| thinking 存放 | `reasoning_details` | `reasoning_details` + `encrypted_content` | `response.content[].thinking` 或 SSE 重建 |
| tool 调用 | `tool_calls`（OpenAI 形） | `tool_calls` | `content[].tool_use` |
| tool 结果 | `role:tool` | `role:tool` | `user` + `tool_result` 块 |
| 顶层 | +`format_kind` | +`format_kind`, `phase`, `responses_type` | 无 session 聚合；逐 call blob |

---

## 6. 与 prior 分析结论的交叉引用

| 话题 | 结论 |
|------|------|
| Opus→Haiku 跨模型 signature 解密 | 样例中 **无**；0 条同 signature 复用解密 |
| 样例「类解密」 | 同会话同模型 adaptive thinking 可见性变化，非攻击链 |
| 论文 thinking hacking Step A（sig-only） | 样例大量存在 |
| 论文 Step B（Haiku transcribe） | 样例 **不存在** |
| TokenKey 实测（fable tool 轮） | 常无 thinking；后续 turn 可出现 sig-only 或 sig+text |

---

## 7. 明日待决（open questions）

1. **产品目标**：是否需要 TokenKey 导出「样例对齐视图」（`reasoning_details` + Chat 形 tool 消息），还是只保留 traj v2 `blocks[]` 作为 canonical？
2. **捕获层**：若要对齐样例，应在 **投影层** 做映射（`blocks.thinking` → `reasoning_details`），而非改 upstream tee。
3. **sig-only 与 `reasoning_content`**：4464 条 mismatch 是否要在投影规则里显式建模（空 text + 非空 signature）？
4. **0808 MASKED 包**：研究用是否接受占位符，还是 opt-in 导出保留 signature 长度但不保留明文？

---

## Validation

- 样例字段 inventory：本地脚本扫描 290 session / 37330 messages（2026-08-19）。
- TokenKey 结构：`backend/internal/observability/qa/service.go`, `trajectory/projection_v2.go`, `projection_v2_test.go`。
- **未验证**：未在本 PR 中重跑 preflight / 未新增自动化测试（纯文档 WIP）。
