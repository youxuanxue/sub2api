---
title: Kiro Auto-Mode Transcript 受限裁剪原型
status: pending
approved_by: pending
risk: high
owners: [tk-gateway]
related_prs: []
related_commits: []
supersedes_note: "补丁关系见 kiro-payload-overflow.md §Pending exception；不替代 900 KiB 本地护栏"
---

# Kiro Auto-Mode / Compact Transcript 受限裁剪原型

## Background

Claude Code auto-mode 安全分类器会把会话 transcript 拼进 **单条 / 少量 user
text**（常带 `<transcript>`、约 140KB 安全 system、`max_tokens≈64`）。TokenKey
译成 Kiro 后 `history_entries=0`，现有
[`kiro-payload-overflow`](kiro-payload-overflow.md) 裁剪链（history 图/轮次、
current tool-result/图片）对 **current user prose** 无能为力，本地 413
`request_too_large`。用户侧常显示为 sonnet / auto mode「不可用」。

Claude Code `/compact`/`/autocompact` 也会把待压缩内容放进最后一个 user 消息的
多个 text block。转换器若先把这些 block 拼接，history 裁剪器看不到可删除的边界。
本原型在同一受控开关下先保留这些边界，再让既有 history 裁剪器按完整 block 删除。

需求分析与证据见
[`../task-breakdown-kiro-auto-mode-transcript-trim.md`](../task-breakdown-kiro-auto-mode-transcript-trim.md)
（《新证据一》结构、《新证据二》容量）。本文件是**原型设计审批基线**（`pending`）：
人工审批前不得默认开启生产改写。

## Delta

### ADDED

- **受限原型路径（默认关闭）**：仅当译成 Kiro 并跑完既有 history/tool 裁剪后
  **仍超过 `maxPayloadBytes`**，且请求被识别为 Claude Code auto-mode 分类器、
  transcript 可完整解析时，才按固定顺序整条删除可丢弃记录并**每删一条重测**；
  未超限请求保持字节级不变（S4）。支持 NDJSON 单块与 marker 分隔的
  content-block 序列两种形状。
- **识别必要条件（同时满足才进入改写；否则保持原文并走现有 413）**：
  1. 入站可观测到 auto-mode 分类器形状（`<transcript>` 成对标记、小
     `max_tokens`、安全分类 system 块等——具体指纹以原型实现与测试夹具为准）；
  2. transcript 内为**可完整解析**的结构化记录（逐行 JSON 或已文档化的等价
     block 序列）；伪造标签、嵌套破坏、角色不明、缺最新 action → **不改写**。
- **删除顺序（唯一允许的减肥动作）**：
  1. 从最旧到最新，整条删除已结束的旧工具过程记录（如 Bash/Read/Write/Edit
     调用及其结果）；每删一条重测尺寸；省略处插入固定占位句；
  2. 仍超限则删除最旧的非授权元数据记录；
  3. 仍超限 → 保持现有本地 413，零上游、不 failover、不罚号。
- **硬保留**：全部真实 `user` 原文；最新 action 及其紧邻 meta/outcome；
  `<transcript>` 外壳与结尾标记；安全 system / 分类器提示；工具 schema。
- **账号开关**：复用 `Account.Extra` 布尔
  `kiro_auto_mode_transcript_trim=true`（仅 Kiro；缺省/非布尔 = 关闭）。无
  schema 变更。关闭时行为与今完全一致。
- **Compact block split**：仅在本地超限、开关开启、最后一个 user 消息包含至少两个
  纯 `text`/`input_text` block、总文本至少 256 KiB 且请求没有 assistant turn 时，
  将每个 block 变成一个独立 user turn；最后一个 block 保留为 current，其余交给既有
  history 裁剪器整条删除。`context_management` 和 `max_tokens` 不再是硬跳过条件，
  因而同一受控开关也覆盖 max_tokens=64 的 auto-mode 请求和没有该字段的 compact
  请求。混合图片/工具 block、单 block、小消息仍保持原文。
- **可观测性**：诊断只报尺寸、删除类别/条数、识别是否命中、hash；不落正文。

### MODIFIED（相对 overflow 契约的**有条件例外**）

- [`kiro-payload-overflow`](kiro-payload-overflow.md) 的
  「不得缩短 current user prose」在本开关开启且识别成功时，允许上述**结构化
  整条删除**所导致的 current 文本变短；**禁止**头尾截断、摘要、模型总结、
  按角色盲删 assistant、或改写用户授权原文。
- 900 KiB（`maxPayloadBytes`）护栏、413 语义、非 failover、不罚号：**不变**。
  抬限到 940 KiB 等容量变更**不在本文件范围**，仍走 overflow / 任务拆解中的
  单独审批。

### REMOVED / 明确不做

- 网关按标签摘要、单条 head/tail 截断 transcript。
- 伪造分类器「允许」结果。
- 对 compact 消息做字符级 head/tail 截断；只允许按完整 text block 删除。
- 超限自动跨平台 failover（newapi/Anthropic）——另案。
- 将「删旧工具过程」宣传为对本批《新证据一》流量的必救方案（《新证据一》
  wire 上零 `tool_use`/`tool_result`，该步收益可能为 0；原型必须用完整样本
  **实测收益**，收益为 0 则不得宣称修复）。

## Scenarios

| # | 类型 | 场景 | 期望 |
|---|---|---|---|
| S1 | 正 | 开关开 + 可解析 auto-mode + 存在可删旧工具过程且删后 ≤900KiB | 改写后通过 `ValidatePayloadSize`，可上游；保留项完整 |
| S2 | 负 | 开关开但解析失败 / 缺最新 action | **原文不动**，413（若仍超限） |
| S3 | 负 | 开关关、单 block、含图片/工具、含 assistant turn，或无法安全识别的普通大文本 | 零改写，现有 overflow 行为 |
| S4 | 回归 | 未超限的小请求 | 字节级与今一致 |
| S5 | 安全 | 先禁止后简短确认、撤回、第三方插入、依赖旧 Write 内容的动作 | 减肥后判定与未改写基线对比；漂移则失败，不得上线 |
| S6 | 正 | 开关开 + compact 多 text block + 删除旧 block 后 ≤900KiB | 保留最新 block，整条删除旧 block 后通过本地尺寸校验 |
| S7 | 负 | 含图片/工具或只有一个 block | 原文不动，沿用 413 |

## Prototype gates（审批前必须可演示）

对齐任务拆解 §原型验收门槛：

1. 格式/版本/角色/转义边界文档化；无法判断则不改写。
2. 完整解析；错误即保持原文。
3. 测试证明 user 原文与最新 action/meta 保留；旧工具过程仅整条、从旧到新删。
4. 受控样本上对比减肥前后分类器判定（允许/拒绝等）。
5. 分列报告：尺寸达标、上游接受、安全判定；有限样本不宣称普遍语义等价。

## Validation

- 单元：识别、NDJSON/content-blocks 解析、按预算停删、解析失败不改写、already_fits。
- 聚焦：`go test -tags=unit` 覆盖本原型包与既有 US-065 overflow 回归（flag 默认关）。
- 网关：仅 `ValidatePayloadSize` 失败且 Extra 开启时进入 compact block split 或
  `TrimClaudeAutoModeTranscriptUntil`；两条路径都在每次完整记录/完整 block 删除后
  重测，413 诊断只带计数与 skip reason。
- 人工：开关账号副本演示 S1–S5；未批准前禁止改生产默认 Extra。
- Story：[`US-071`](../../.testing/user-stories/stories/US-071-kiro-auto-mode-transcript-trim.md)；
  本 `pending` 文档无 related_prs，直至实现 PR 与人工 merge 审批。

## Scope Boundary

不修改计费、调度优先级、DB schema、公开 API 字段、默认开启策略。
Web impact: none。

## Community survey (2026-10-26)

开源中转/社区对照结论（完整报告在会话调研；此处只固化决策约束）：

- **没有任何**已检索仓库实现「识别 Claude Code auto-mode `<transcript>` → 安全等价减肥」且可复用。
- 主流只裁外层 `history`（kiro-lb / jwadow/kiro-gateway / KiroProxy 等）；对 **history 空、体积在 current user** 的本批 413 **零收益**。
- 多家明确拒绝静默裁剪（kirogo「never trimmed」、jwadow 默认 `AUTO_TRIM=false`、pi-provider fail-closed）。
- Kiro-Go 的 **硬截 current message**、KiroProxy **AI 摘要历史** → 与本文件「禁止头尾/摘要」同向否决，不得引入。
- 客户端有界分类（iTerm2、claude-auto-permission、CC 自身投影）证明正确修复面在**拼装方**；不能外推为网关可任意改官方 transcript。
- 未见「超限静默改打 Anthropic」的成熟开源先例；跨供应源须产品显式路径，另案审批。

因此本原型定位为：**强门禁实验**，须用完整样本分报「尺寸 / 上游 / 安全判定」；若 wire 仅有伪装成 user text 的拼接、无可解析行级工具记录，应诚实标「对本批流量未解决」，不得改用标签裁剪凑绿。
