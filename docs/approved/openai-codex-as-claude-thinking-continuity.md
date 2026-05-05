---
title: OpenAI Codex 伪装 Claude — Thinking 多轮连续性修复
status: shipped
approved_by: xuejiao (design review 2026-04-22)
approved_at: 2026-04-22
authors: [agent]
created: 2026-04-22
related_prs: ["#31"]
related_commits: [922dd169]
related_stories: [US-027]
---

# OpenAI Codex 伪装 Claude — Thinking 多轮连续性修复

## 0. TL;DR

当 `claude-opus-4-7` 请求被路由到 OpenAI Codex 账号（GPT专线，`platform=openai` + `channel_type=0`）时，含 `thinking` 的多轮（`assistant: [thinking, text, tool_use…]` → `user: [tool_result…]`）会触发上游 **静默返回 0-token 空消息**，CC 客户端把它显示为"答完了，请继续"，用户必须不停 `继续`，且每次"继续"也只回一两句。

**根因**：`AnthropicToResponses` 同时设置了 `Include=["reasoning.encrypted_content"]` 和 `Store=false` —— 这是 Responses API "无状态思考续接"模式，要求 **下一轮把上一轮产生的 `reasoning` item 原样回放**；但我们：

1. 上行 (Codex `reasoning` → Anthropic `thinking_delta`) 只保留明文 text，丢了 `id` 和 `encrypted_content`；
2. 下行 (Anthropic `thinking` block → Responses input) 在 `anthropicAssistantToResponses` 里**直接 ignore**；
3. 上游 Codex 收到"有 `function_call_output` 但缺对应 `reasoning` item"的输入，按规范静默返回空消息。

**修复**：分两步。

- **Part A（护栏）**：流式 Responses→Anthropic 转换路径保证 terminal event 前至少发出一个 `content_block`。最终实现采用常数空间的 emit flag + 空 text block schema firewall，而不是早期方案里的流式 `BufferedResponseAccumulator`。
- **Part B（根治）**：去掉 `Include=["reasoning.encrypted_content"]`，让 Codex 不再产生加密 reasoning 状态、也不再要求回放。每轮重新思考的代价（多花 reasoning token、首 token 略慢）显著小于"空响应让用户狂按继续"。

> **范围**：本设计**只**修复"OpenAI 平台账号承接 Anthropic 协议请求"这一条复合路径。**不**改原生 Anthropic 平台 (`platform=anthropic`)、**不**改 Chat Completions compat 路径、**不**做 `previous_response_id` 状态化转发（会引入会话级映射存储，超出当前修复半径）。

## 1. 现状盘点

### 1.1 触发条件

只有同时满足以下条件才会触发空响应：


| 条件                                                     | 检查点                                                    | 现状                                                                   |
| ------------------------------------------------------ | ------------------------------------------------------ | -------------------------------------------------------------------- |
| 路由到 OpenAI 账号                                          | `group.platform == openai`、`account.channel_type == 0` | `gateway_tk_openai_compat_handlers.go:tkOpenAICompatMessagesPOST` 派发 |
| 走 Anthropic Messages 协议                                | 路径 `POST /v1/messages`                                 | `OpenAIGatewayHandler.Messages`                                      |
| 客户端发了 `thinking` 块                                     | `messages[i].content[*].type == "thinking"`            | CC 默认开启 thinking 时会回放                                                |
| 历史里有 `tool_result`                                     | `messages[i].content[*].type == "tool_result"`         | 多轮工具调用必然出现                                                           |
| 上游 `Include=reasoning.encrypted_content + Store=false` | `AnthropicToResponses` 硬编码                             | `anthropic_to_responses.go:30,33`                                    |


### 1.2 实证数据（prod EC2，2026-04-22）

抽 1 小时内 11 次相关请求：


| request_id 前缀 | 形态                 | duration_ms | input/output_tokens | content blocks                                        |
| ------------- | ------------------ | ----------- | ------------------- | ----------------------------------------------------- |
| `d47363ad`    | 首轮（无历史）            | 28000       | 正常                  | thinking + text + 7×tool_use → `stop_reason=tool_use` |
| `d96119c3`    | 工具续接 + thinking 历史 | 692         | 0 / 0               | **空** → `stop_reason=end_turn`                        |
| `bb8d9107`    | 同上                 | 1100        | 0 / 0               | **空**                                                 |
| `ba3e51e8`    | 同上                 | 1500        | 0 / 0               | **空**                                                 |


3 个空响应的请求体共同特征：`messages[-2].content` 含 `thinking`，`messages[-1].content` 含 `tool_result`。

### 1.3 已有基础设施（不动）


| 模块                                            | 文件:行                                                        | 作用                       |
| --------------------------------------------- | ----------------------------------------------------------- | ------------------------ |
| `BufferedResponseAccumulator`                 | `apicompat/responses_buffered_accumulator.go`               | 已为非流式 buffered 路径兜底，单测齐全 |
| `handleAnthropicBufferedStreamingResponse` 兜底 | `openai_gateway_messages.go:295,315,350`（commit `a1e299a3`） | 非流式路径已修                  |
| `responseHeaderFilter` / sticky session       | 无关                                                          | 不动                       |


### 1.4 已有 Bug 路径（要改）


| 模块                                 | 文件:行                                      | 现状                                                     |
| ---------------------------------- | ----------------------------------------- | ------------------------------------------------------ |
| `handleAnthropicStreamingResponse` | `openai_gateway_messages.go:375-588`      | **没有**镜像 a1e299a3 的兜底；没有 `BufferedResponseAccumulator` |
| `AnthropicToResponses.Include`     | `apicompat/anthropic_to_responses.go:30`  | 硬编码 `["reasoning.encrypted_content"]`                  |
| `anthropicAssistantToResponses`    | `apicompat/anthropic_to_responses.go:235` | 注释 `// thinking blocks → ignored`                      |


### 1.5 同因证据（外部）

- **Wei-Shaw/sub2api 0.1.112**（4/13 release）已合入 *"修复 Anthropic 非流式路径在思考模式下上游终态事件 output 为空时 content 字段返回为空"* —— 与我们 commit `a1e299a3` 同因，旁证根因分析无误；但上游同样**没修流式路径**，仍是行业空白。
- `**anthropics/claude-code#24662`**：客户端在收到含空 content block 的 assistant 消息时，会把损坏帧 persist 到 `~/.claude/projects/*/*.jsonl`，**之后每一次 retry / `/compact` / `继续` 都会重发损坏历史 → 整个会话 brick**，只能手工截断 JSONL。这把本 bug 的影响面从"UX 退化"升级为"会话锁死"，加重修复优先级。

## 2. 设计

### 2.1 Part A — 流式空内容兜底（防御纵深）

把 `handleAnthropicStreamingResponse` 改造成：

```text
NewBufferedResponseAccumulator() acc
state := NewResponsesEventToAnthropicState()
for each upstream SSE event:
    acc.ProcessEvent(event)            ← 新增：累加 deltas
    events := ResponsesEventToAnthropicEvents(event, state)
    for evt := range events:
        write SSE to client

on stream end / response.completed / response.done:
    if state.NeverEmittedContentBlock && acc.HasBufferedText():
        synthesize content_block_start(text, idx=0)
        synthesize content_block_delta(text_delta, acc.Text())
        synthesize content_block_stop(idx=0)
        write SSE to client
    finalize message_delta + message_stop
```

`state.NeverEmittedContentBlock` 是新增的状态字段，在 `closeCurrentBlock` 内置位检查。

**作用**：把 a1e299a3 的"非流式上游 output 为空但 deltas 有内容"防御纵深扩展到流式路径。在我们当前的空响应案例里 deltas 也是空的，所以这一项**不会直接消除**问题，但能阻止未来上游协议变化导致的"deltas 有内容 / 终态 output 空"这种相邻 bug 模式。**结合 §1.5 的 `claude-code#24662` 风险**——空 content block 会污染 CC 本地 JSONL 并在后续每次重试时回放——Part A 把"网关侧绝不向 CC 吐出零 content block 的 assistant 消息"上升为硬不变量，相当于在客户端外加一层 schema 防火墙。

> 命名 `NeverEmittedContentBlock` 比 `WroteAnyContentBlock` 更显意图——出问题的是"零产出"分支。

### 2.2 Part B — 关闭 reasoning 加密续接（根治）

把 `AnthropicToResponses` 改成：

```diff
 out := &ResponsesRequest{
     Model:       req.Model,
     Input:       inputJSON,
     Temperature: req.Temperature,
     TopP:        req.TopP,
     Stream:      req.Stream,
-    Include:     []string{"reasoning.encrypted_content"},
+    // 不请求 encrypted_content，因为我们当前不会把它 roundtrip 回 input。
+    // 一旦 Include + Store=false 同时存在，上游会要求每个 function_call_output
+    // 前都跟一个对应的 reasoning item，否则静默返回空消息。
 }

 storeFalse := false
 out.Store = &storeFalse
```

**效果**：上游 Codex 不再生成加密 reasoning 状态，也不再期望客户端回放 → 多轮工具续接稳定返回完整内容。

**代价分析**：


| 维度                       | 现状（带 Include）  | 修复后（不带 Include）       | 评估                   |
| ------------------------ | -------------- | --------------------- | -------------------- |
| 单轮 reasoning token       | 同              | 同（实际产生量取决于 effort，不变） | 持平                   |
| **多轮**累计 reasoning token | 理论上低（可续接）      | **略高**（每轮重新思考）        | 可接受：CC 多轮远远好过空响应     |
| 首 token 延迟               | 低（少思考）         | 略高（每轮重启思考）            | 与原生 Claude Opus 行为一致 |
| 用户体验                     | **空响应 + 频繁继续** | 完整响应、不需要继续            | **质变**               |


### 2.3 显式不做（Non-goals）

- **不做** Option B2（`thinking.signature` 字段编码 `(reasoning_id, encrypted_content)` 双向 roundtrip）。理由：
  - `AnthropicContentBlock` 没有 `Signature` 字段，需要扩展 type；
  - `signature` 在 Anthropic 真实协议里是不透明 base64，长度可达数 KB，塞入响应流会撑大每个 thinking 块、改变客户端可观察行为；
  - 我们不能保证 OpenAI 的 `encrypted_content` 在跨账号、跨上游 endpoint 之间能复用（账号 1 产生的 encrypted state 放进账号 2 的请求里大概率被拒）。
  - `icebear0828/codex-proxy` 的实现走的是 *WebSocket + `previous_response_id*` 这条更重的路（见 §10），跟我们的 stateless HTTP SSE 转发架构差距大，不适合一次性吞下。
  - 待 OpenAI 文档明确"跨 session 续接"语义后再评估。
- **不做** `Store=true` + `previous_response_id`：需要在 gateway 维护 `(client_session, last_response_id)` 映射 + Redis TTL，超出本次修复半径。
- **不做** "空响应自动换号重试"（`codex-proxy` 在 §10 里有此实现，最多 3 次切账号）：本次 fix 把空响应**从根上消除**，retry 是治标层；如果 §6 观测显示残余空响应仍非零，再单独立项。
- **不动** 原生 Anthropic 路径 (`platform=anthropic`)：那条路完全没有这个 bug。
- **不动** 已有的 affinity / sticky session 机制：与本次修复正交。
- **不动** WSv2 forwarder（`openai_ws_forwarder.go`）：那是 `Wei-Shaw/sub2api#1506` 反向 bug 的代码（多轮串台），完全独立路径；混改会污染本次最小修复半径。

### 2.4 配置与开关

不引入新的 config flag。理由（Jobs 简洁原则）：

- 这是 bug fix，不是 feature；
- 加 flag 等于把"是否该正常工作"变成可调，偏离 OPC "唯一规范路径"原则；
- 如果 OpenAI 上游未来改变行为又需要 Include，再加。

## 3. API 行为变化（对外契约）


| 维度                                    | 变化前                            | 变化后                                         |
| ------------------------------------- | ------------------------------ | ------------------------------------------- |
| `POST /v1/messages` (OpenAI 路由) 上行响应  | thinking blocks 不含 `signature` | 不变（本来就没塞）                                   |
| `POST /v1/messages` (OpenAI 路由) 多轮空响应 | 触发                             | **不再触发**                                    |
| 单轮 token 计费                           | 同                              | 同                                           |
| 多轮累计 reasoning token                  | 较低                             | 略高（持平于"每轮独立"）                               |
| 流式 SSE 事件序列                           | 同                              | 同（Part A 只在 deltas 非空但 content block 零产出时补） |
| 上游对 Codex 账号配额消耗                      | 较低                             | 略高（重叠思考）                                    |


**契约文档同步**：`docs/agent_integration.md` 端点表本身不变（接口形状未动）；不必重生成。

## 4. 数据模型 / Schema

无变更。不涉及 ent schema、不涉及 migration、不涉及新表/列。

## 5. 测试策略

### 5.1 测试故事

新建 `US-027` `openai-codex-as-claude-thinking-continuity`：

- AC-001（正向 / 现有不变）：单轮 `claude-opus-4-7` 请求经 OpenAI 路由仍能正常返回带 thinking + text 的内容。
- AC-002（**根因正向**）：多轮 `assistant: [thinking, text, tool_use]` → `user: [tool_result]` 经 OpenAI 路由后，**不再返回 0-token 空响应**，content 至少含一个 `text` 或 `thinking` 块、`output_tokens > 0`。
- AC-003（防御纵深）：上游模拟"deltas 有内容但 `response.completed.output` 为空数组"时，client 收到的 SSE 至少含一个 `content_block_start(text)` + `content_block_delta(text_delta)` + `content_block_stop`。
- AC-004（负向 / 回归）：`Include` 字段从上游 Responses 请求体里**消失**（Snapshot 断言）；不要回归到旧值。
- AC-005（回归）：原生 Anthropic 路由 (`platform=anthropic`) 的 thinking 多轮不受影响（snapshot 无变化）。

### 5.2 测试形式

- **单元测试**：`apicompat/anthropic_to_responses_test.go` 增 case 验证 `Include` 不在生成的 ResponsesRequest 里；`responses_to_anthropic_test.go` 增 case 验证流式 SSE 在 deltas-only 场景下能补出 content block。
- **集成测试**：`backend/internal/service/openai_gateway_messages_us027_test.go`（go test `-tags=integration`），用 mock Codex SSE server 复现"`response.completed` output 为空 + 之前没有 deltas"，断言 client 拿到的 SSE 至少包含完整的 message_start → content_block_* → message_delta(stop_reason) → message_stop。
- **手工冒烟**（非 CI）：把分支部署到 `test-api.tokenkey.dev`，用 CC 跑一轮含 `thinking + 多 tool_result` 的真实任务，确认 `qa_records` 里同形态请求的 `output_tokens > 0`。

### 5.3 mock 选择

集成测试用 in-process `httptest.Server` 模拟 Codex Responses SSE，**不**依赖真实 ChatGPT 订阅账号（OPC 自动化原则：CI 必须能跑）。

## 6. 部署 & 回滚

- **部署形态**：常规 prod 发版（`v*` tag → `release.yml` → ARM 多架构镜像 → AWS test+prod 拉取）。
- **回滚**：单 commit revert（Part A + Part B 都是局部 diff，约 50 行）；revert 后行为退回"空响应 bug 仍在"，无数据迁移、无状态污染。
- **观测**：发版后 1 小时 grep prod `qa_records` 里 `claude-opus-4-7` 路由 `platform=openai` 的请求，确认 `output_tokens=0` 比例从约 27%（3/11）降到 0。

## 7. 风险与缓解


| 风险                                            | 概率  | 影响                                          | 缓解                                                                                                                      |
| --------------------------------------------- | --- | ------------------------------------------- | ----------------------------------------------------------------------------------------------------------------------- |
| Codex 不带 Include 时表现退化（reasoning 质量下降）        | 低   | 单轮 reasoning 能力本就由 `effort` 控制，与 Include 无关 | 监控 user feedback；可灰度先在 test-api 跑一周                                                                                     |
| 多轮 reasoning token 上涨过多                       | 中   | 用户配额消耗加快                                    | 计费侧已按上游 `output_tokens` 计；监控周环比                                                                                         |
| Part A 兜底误触发（deltas 有内容但 content block 已正常发出） | 极低  | 重复内容                                        | `NeverEmittedContentBlock` 标志位只在零产出时才补，严格守门                                                                             |
| 漏掉某条已存在的 `Include` 调用方                        | 低   | 部分路径仍触发空响应                                  | grep `Include.*reasoning.encrypted_content` 全仓确认只有这一处                                                                   |
| 回归 `Wei-Shaw/sub2api#1506`（WSv2 多轮串台）         | 极低  | 上一轮残留串入下一轮                                  | 本 PR 完全不动 `openai_ws_forwarder.go` 与 session state store；diff 范围限定在 `apicompat/` + `service/openai_gateway_messages.go` |
| 长上下文 (>500K) 仍有空响应 / 答非所问                     | 中   | 残余 UX 退化                                    | 非本 PR 责任（社区共识为模型自身退化，见 §10）；建议 follow-up 加文档教用户在 CC 里设 `CLAUDE_CODE_AUTO_COMPACT_WINDOW`，或在调度层加上下文长度软上限                 |
| 修复后历史已 brick 的 CC 会话仍无法恢复                     | 高   | 个别用户必须手工截断 JSONL                            | 在 release notes 链接 `claude-code#24662` 的截断脚本；属客户端侧问题，gateway 修复后**新会话**不再产生                                             |


## 8. 实施清单（实现阶段使用）

1. `apicompat/anthropic_to_responses.go`：删除 `Include: []string{"reasoning.encrypted_content"}`，加同上注释。
2. `apicompat/responses_to_anthropic.go`：在 `ResponsesEventToAnthropicState` 加 `NeverEmittedContentBlock bool`，初值 true；在 `closeCurrentBlock` 之前的所有 `content_block_start` emit 处置 false。
3. `service/openai_gateway_messages.go:handleAnthropicStreamingResponse`：注入 `BufferedResponseAccumulator`，每事件 `acc.ProcessEvent`；在 `finalizeStream` 前如 `state.NeverEmittedContentBlock && acc 有非空文本`，合成 3 个 SSE 事件。
4. 新增单测 + 集成测 + Story 文档。
5. `make test` 全绿；`./scripts/preflight.sh` 全绿。
6. 提交 PR：`fix: prevent empty Codex responses on Claude thinking + tool_result rounds`。

## 9. 当前实施状态

- 已 shipped：PR #31 / commit `922dd169` 落地根因摘除与流式空内容 schema firewall。
- 当前 Story：`US-027-openai-codex-as-claude-thinking-continuity.md` 仍处于 InTest，记录本地单测证据与 prod 部署证据待补。
- 当前实现偏差：§2.1 的早期 `BufferedResponseAccumulator` 流式兜底设计已收敛为常数空间的 emit flag + 空 text block 护栏；非流式 buffered accumulator 仍保留在 Responses→Chat Completions 路径。

## 10. 同类项目实践借鉴（决策依据）

为避免闭门造车，调研了 Linux.do 社区与三个直接相关项目的 prod 经验。结论：本设计选择的 Part A + Part B 与同类项目最佳实践一致，但**刻意选择了更小的修复半径**，把更重的状态化方案推后到 follow-up。

### 10.1 `icebear0828/codex-proxy`（最相关 — 同类项目）

一个独立实现的 *Codex 伪装 Anthropic* 代理（TS/Bun），生产里跑过较大流量。其 CHANGELOG 显示与本 bug 高度同源的几个生产对策：


| 实践                                                                                                              | 现状                                              | 我们的取舍                                                                 |
| --------------------------------------------------------------------------------------------------------------- | ----------------------------------------------- | --------------------------------------------------------------------- |
| **空响应自动换号重试**（HTTP 200 + 无内容 → 切账号最多 3 次；流式注入错误文本而非静默）                                                          | 已上 prod，配 per-account `empty_response_count` 指标 | 先不抄；§2.3 已说明：本 PR 从根因消除空响应，retry 是治标层                                 |
| `**reasoning.encrypted_content` Include 只在 reasoning 开启时设置**，且配 WebSocket + `previous_response_id` 真做 roundtrip | 已上 prod                                         | 我们的 stateless HTTP SSE 架构无法低成本做 roundtrip → 直接**不要** Include（更激进、更安全） |
| `**default_reasoning_effort` 默认 → `null`** （早期默认 medium 触发简单对话 token 暴涨，被社区投诉后改）                                | 已上 prod                                         | 不在本 PR 范围；纳入 follow-up，复查我们当前 `effort=high` 默认是否合理                    |
| **流式 SSE 不设 `--max-time` 墙钟超时**（修复思考链 60s 截断）                                                                   | 已上 prod                                         | 我们 grep `openai_gateway_messages.go` 确认无 wallclock timeout，无需改        |
| **Always `summary: "auto"`**，让 Codex 吐 reasoning summary 事件                                                     | 已上 prod                                         | 我们已经这么做（参 `apicompat/anthropic_to_responses.go`）                      |
| **Schema 放行 `thinking` / `redacted_thinking` 块**（CC `/compact` 回放历史会带这两类）                                       | 已上 prod                                         | 已支持；不动                                                                |


### 10.2 `Wei-Shaw/sub2api`（我们的 fork 上游）

对比上游近 1 个月 release：

- **0.1.112 (4/13)**：*"修复 Anthropic 非流式路径在思考模式下上游终态事件 output 为空时 content 字段返回为空"* —— 与本仓 commit `a1e299a3` **同因**，旁证根因分析无误。但上游同样**只修了非流式路径**，流式路径仍是空白，本 PR 的 Part A 在上游也是新增价值。
- **0.1.111 (4/12)**：*"Messages 模型映射：支持 messages 模型映射与 instructions 模板注入"* —— 我们已合入此能力（`backend/internal/service/openai_codex_instructions_template.go`）。如果 §6 观测显示 Part B 后还有边缘空响应，可在 instructions 模板里加一句 *"continue thinking and produce content in the same turn"* 作为压力释放阀，无需改代码。
- **#1506 "codex 重复回答问题"**（4/8 至今 open）：**反向 bug** —— WSv2 路径多轮串台，Q-c 响应里混入 Q-b 残留。根因相反（state 复用过度，而非过少），代码完全在另一条路径（`openai_ws_forwarder.go`）。本 PR 已在 §2.3 / §7 明确不动这条路径，避免任何相互回归。社区里 `@Woo0ood` 等用户额外观察：**长上下文 (>500K)** 时该问题更频繁，倾向于"模型自身长上下文退化"而非纯网关 bug——这条与我们用户上下文 (~120K) 不冲突，但建议在用户文档里推荐 `CLAUDE_CODE_AUTO_COMPACT_WINDOW≈120K` 防长上下文劣化。

### 10.3 `anthropics/claude-code#24662`（客户端侧风险放大器）

CC 客户端缺三类校验（issue 作者归纳）：

1. **Pre-persistence validation**：streaming 收到的空 content block 直接落盘，没 schema check；
2. **No error recovery**：API 拒绝后下一次 retry 仍重发损坏历史；
3. **No session repair on load**：`claude --continue` 时不扫描损坏帧。

**对我们的启示**：不能把"空响应"看成只是 UX 退化。一旦客户端把"零 content block 的 assistant 消息"持久化到 JSONL，**用户每次按"继续"都在重发损坏历史**——这正是用户报告"每次继续都很少"的次级原因（每条新请求都被卡在重发损坏历史的 retry loop 里）。Part A 把"网关侧绝不向客户端吐出零 content block 的 assistant 消息"上升为硬不变量，等于在客户端外加一层防火墙。

### 10.4 Linux.do 社区共识（经搜索引擎抽取，原帖被 CF 挡）

- `**linux.do/t/796869`**：CC 配第三方 API 容易截断/被 5xx —— 高频归因为"输入 token 涨到一定阈值后上游过载"或"代理侧默认截断"。
- `**linux.do/t/1957168`**（被 #1506 引用）：长上下文下多个 LLM 都会出现"答非所问 / 重复输出"—— 倾向模型层退化。

这些和本 bug 不同因（我们的 input 才 ~120K，远低于 500K 阈值），但说明社区**同时存在多个独立失败模式**，定位时不应被合并。本 PR 严格框定在 *"Codex Responses API + thinking + tool_result 多轮"* 这一路径。

### 10.5 与本 PR 设计选择的对照


| 决策                     | 我们的选择              | 同类项目先例                             | 偏差理由                                                 |
| ---------------------- | ------------------ | ---------------------------------- | ---------------------------------------------------- |
| 流式空内容兜底                | Part A 加防御         | 无项目明确做（icebear 走的是空响应 retry）       | a1e299a3 已证明非流式有效；流式镜像零成本                            |
| Reasoning 加密续接         | Part B 直接关 Include | icebear: 关闭 + WS 状态化；Wei-Shaw: 还没动 | 我们的 stateless 架构不支持 WS roundtrip；关 Include 是最低风险有效方案 |
| 自动换号 retry             | 不做                 | icebear 做                          | 治标层，待 §6 残余空响应观测再评估                                  |
| 默认 reasoning effort 调整 | 不在本 PR             | icebear 已改 null                    | 独立优化项；本 PR 不混入                                       |
| Instructions 模板加压力释放   | 不在本 PR             | 上游 0.1.111 已提供能力                   | 备用方案；如残余问题则启用                                        |


