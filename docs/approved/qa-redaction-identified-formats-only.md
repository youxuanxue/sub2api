---
title: QA 脱敏仅覆盖已识别格式
status: approved
approved_by: "user (2026-09-11: 继续，安全推进)"
approved_at: 2026-09-11
created: 2026-09-11
authors: [codex]
risk: high
---

# 背景

QA 采集会对请求、响应和流式片段做自动脱敏。当前非结构化路径对每段文本连续运行多条正则，导致大响应中的普通文本消耗较高 CPU。目标是在保持已识别凭证形态覆盖的前提下，减少对明显普通文本的重复扫描。

本设计接受一个明确的安全边界变化：**无法确认属于已支持格式的文本直接跳过脱敏**。因此，未知格式中的秘密可能原样进入 QA 归档或导出；系统不再宣称对未知格式提供正文秘密发现能力。

# 做什么

- JSON 对象、数组和字符串叶子继续走结构化脱敏。
- 敏感 key 名及其子树继续脱敏；已被敏感 key 替换为 `***` 的子树不再重复扫描。
- SSE `data:` 事件在能确认是 JSON 时走结构化脱敏。
- 能确认是受支持 assignment 格式（`key=value` 或 `key: value`）的内容使用单遍扫描器处理。
- 已知凭证形态（例如 `Bearer`、`sk-`、私钥头、provider token、`AIza`、`GOCSPX`）继续覆盖。
- 明确不属于上述格式的文本直接原样保留，以避免无谓的正则扫描。

# 不做什么

- 不实现通用 DLP、熵分析、词典匹配或任意自定义凭证格式发现。
- 不对无法解析的 JSON、未知 SSE 协议、自由文本中的自定义 key/value 组合做旧正则兜底扫描；只有已经识别为敏感 assignment、known token 或配置的 `extraKeys` 时才进入对应扫描路径。
- 不扩大 QA 采集范围，不新增归档、删除或导出 owner。
- 不把“未命中识别器”解释为“安全”；它只表示该输入超出本版本的保护覆盖范围。

# 判定流程

```text
输入
  ├─ 可解析 JSON？        → RedactJSONValue（结构化 key + 已知 token）
  ├─ SSE data 事件为 JSON？→ 只替换 data payload，保留 SSE framing
  ├─ 命中已知 token 形态？ → 已知 token 扫描器
  ├─ 受支持 assignment？  → 单遍 assignment 扫描器
  └─ 其他                  → 原样保留，不运行旧正则链
```

“可解析”只指整个输入通过标准 JSON 解码；截断、混合协议或未知编码不进入结构化树。解析失败时仍可运行独立的已知 token 扫描器，或在明确命中受支持 assignment 时运行 assignment 扫描器；除此之外原样保留，不回退旧正则链。

## 识别契约

- JSON 只接受完整的 RFC 8259 值。对象 key 使用现有 `defaultSensitiveKeys`、敏感后缀和调用方传入的 `extraKeys`，大小写不敏感；敏感 key 的值直接替换为 `***`，不再递归扫描。
- SSE 按空行分隔事件；为了保持 framing，只有恰好一行 `data:` 且该 payload 整体通过 JSON 解码时才替换该 payload，`event:`、`id:`、注释和空行原样保留。多行 `data:`、截断事件和未知字段组合视为未知格式，只接受已知 token/assignment 规则，不做旧正则兜底。
- 已知 token 集合与现有 `logredact` 模式保持一致：私钥头 `-----BEGIN ... PRIVATE KEY-----`、大小写不敏感的 `Bearer` 加 token、`GOCSPX-`（至少 24 个字符）、`AIza`（后接 35 个字符）、provider token（`glpat-`、`gh[pousr]_`、`github_pat_`、`sk-`、`AKIA`、`ASIA`、`LTAI` 及现有长度约束）。这些模式在任何输入格式上都可扫描，但不扩展为通用正则 DLP。
- assignment 只识别现有敏感 key、敏感后缀或 `extraKeys` 的 `key=value`、`key: value`、JSON-like key 形式；未知自定义 key、非完整 assignment 和无法确认边界的文本直接保留。ASCII 标识符形 key 使用单遍扫描器；含 Unicode/特殊标点的已知 key 为兼容现有覆盖可走窄范围正则路径，不视为未知格式。
- 分类结果是内部实现细节，不新增对外 API；现有 `RedactText`/`RedactJSON` 返回类型保持不变。SSE 处理由 QA service 保持 framing 和 thinking signature 回填契约。

# 数据与接口契约

- QA record、blob 和导出 JSON 的结构不变；`redactions` 元数据中的版本值从 `logredact-v3` 切换为 `logredact-v4`。
- `sanitizeQABytes`、`sanitizeQABody`、`RedactText` 和 `RedactJSON` 的参数及返回类型不变；实现只新增包内分类/扫描辅助函数。
- 内部分类枚举固定为 `json`、`sse_json`、`assignment`、`known_token`、`unknown_passthrough`，仅用于计数和测试，不写入正文或用户可控字段。
- 版本变更必须同步 [`scripts/sentinels/redaction.json`](../../scripts/sentinels/redaction.json)；实现入口、QA service 调用点和 focused 回归测试必须同步更新 [`scripts/sentinels/gateway-tk.json`](../../scripts/sentinels/gateway-tk.json)。若上游共享文件继续有冲突，优先采用 `*_tk_*` companion 或纯追加入口，不能静默丢失脱敏行为。

# 版本与兼容

- `qaRedactionVersion` 固定升级为 `logredact-v4`，便于导出方和审计区分新旧保护边界。
- 已有 QA blob 不重写、不迁移。
- QA 导出元数据继续携带脱敏版本；实现 PR 同步更新 [`security-capture-and-ingress.md`](security-capture-and-ingress.md)、[`qa-bundle-session-export.md`](qa-bundle-session-export.md) 及产品隐私说明，明确“自动脱敏仅覆盖已识别格式，未知格式可能包含秘密”。

# 验证与观测

- 单元测试覆盖：结构化敏感 key、JSON 字符串叶子、SSE JSON、assignment、每种已知 token 形态，以及未知格式原样保留。
- differential 测试确认识别格式的输出与当前安全基线一致；未知格式明确验证不调用旧正则链。
- fuzz 测试覆盖截断 JSON、畸形 SSE、嵌套数组和长普通文本，要求不 panic 且不误判为结构化输入。
- benchmark 对比大响应普通字符串叶子、SSE JSON 和 assignment 三类输入的 CPU/分配。
- 增加固定枚举的识别路径计数（`json`、`sse_json`、`assignment`、`known_token`、`unknown_passthrough`），不记录正文内容，不使用用户输入作为 label。
- 性能验收要求：普通字符串叶子和 `unknown_passthrough` 不调用旧的 `ReplaceAllString` assignment 正则链；只有命中 JSON key、assignment 或已知 token 的输入才进入对应扫描器。benchmark 必须同时报告吞吐、CPU 和分配，并与 `logredact-v3` 基线比较。

# 回滚

若线上发现已识别格式漏脱敏，回滚代码版本即可恢复旧逻辑。若仅发现未知格式泄露，不能通过配置补救；需要撤回该设计并恢复“未知格式走旧正则兜底”，随后重新发布。

# 审批项

- 是否接受未知格式可能原样落盘的安全边界？
- 是否同意升级 `qaRedactionVersion` 并同步隐私/QA 导出说明？
- 上述识别格式和测试/观测范围是否足够作为实现约束？
