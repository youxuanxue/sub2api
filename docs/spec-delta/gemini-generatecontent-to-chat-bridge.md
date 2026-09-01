# Gemini generateContent → chat_completions 协议边（WIP 评估）

> **状态**：WIP / 待评估。本文只沉淀决策材料，**不**表示已批准实现。  
> **路径**：`docs/spec-delta/gemini-generatecontent-to-chat-bridge.md`  
> **无 Web 影响**：纯文档。`Web impact: none` / `no-web-impact`。

## Background

全能 key 用户走 `/v1beta/models/{id}:generateContent` 时，供应源侧可能存在「mapping 声明了 gemini-*、但账号只讲 OpenAI `chat_completions`」的账号（例：CloudWise 受管账号 `#113`）。

当前协议 SSOT（`backend/internal/engine/protocolrouter/registry.go`）对 Gemini 的边是**不对称**的：

| Inbound | Target | 现状 |
|---|---|---|
| messages / chat / responses | `gemini_generate_content` | 有（往 Gemini 收） |
| `gemini_generate_content` | `gemini_generate_content` | 有（identity） |
| `gemini_generate_content` | chat / messages / responses | **没有** |

因此：即使用户请求的模型在 `#113` 的 mapping 里，Plan 也无法把 generateContent 落到只会 chat 的账号——不是「少一个翻译函数」这么简单，而是 **合法路由边未建立**。

提议（待评估）：在不动账号「原生协议」声明的前提下，补一条 conversion：

`gemini_generate_content → chat_completions`

使入口形状与账号 Target 协议不一致时，走与现有 messages↔chat 同构的 bridge，而不是伪造账号具备 native Gemini capability。

### 与现有 SSOT 的关系（澄清）

现有模型本就是：

1. 账号声明 **SupportedProtocols**（真实 **Target**）
2. 请求带 **InboundProtocol**
3. Plan 选 `Inbound → Target`：同协议 identity，不同则 conversion adapter

例：账号会 `messages`、客户端打 `chat` → `chat_completions_to_messages`。  
因此「声明原生协议 + 异形入口走 bridge」**不是新哲学**。

放到 Gemini 上：**路由模型没有本质区别**；与现有 SSOT 的本质差是 **反向边还没建**，不是世界观不同。

已有同向先例（往 Gemini 收）：`messages_to_gemini_generate_content` / `chat_completions_to_gemini_generate_content` / `responses_to_gemini_generate_content`。  
CloudWise 另有运营向的 anthropic messages → chat fallback（近亲双栈），保真度压力小于 Gemini native ↔ OpenAI chat。

## Delta（提议，未落地）

ADDED（若批准实现）：

- registry 增加 `AdapterGeminiToChat`（名称待定）：`ProtocolGeminiGenerateContent` → `ProtocolChatCompletions`，`RouteConversion`
- 对应 adapter 实现：请求/响应（含 stream）双向映射；usage 字段映射
- `preservesGeminiToChat`（或等价）：能力子集 gate（建议默认 text-only、无 tools / 无 reasoning 粘性承诺时与现有 `preservesToGemini` 同严或更严）
- 账号/供应商窄门：避免「任意 chat 账号」承接 generateContent（见 Risks）
- Plan 优先级：Gemini **identity** 优先于 conversion fallback
- 日志/观测显式标注 conversion，禁止当 identity 排障

MODIFIED（若批准实现，通常需联动）：

- 全能 key + `/v1beta` 路径上「newapi 非 Vertex 拒」等硬门，改为「允许窄门内的 chat Target + conversion」
- CloudWise（或同类）`gemini-` 模型前缀 / mapping 准入，与 hard gate 对齐
- ShapeGemini / 候选准入：区分「native Gemini 账号」与「chat Target + gemini→chat 边」

REMOVED：无（默认不删现有 Gemini identity 路径）。

非目标（明确不做，除非另开决策）：

- 把 `#113` 改造成 native `gemini_generate_content` 账号
- 宣称 CloudWise chat 与 Vertex/Antigravity 全语义等价
- 宽开放「任意 OpenAI-compat 账号自动承接全部 generateContent」

## Scenarios

### 核心正向（若落地）

- Given 账号仅 SupportedProtocols 含 `chat_completions`，且模型在 mapping / 窄门白名单内  
  When 全能 key 打 `/v1beta/...:generateContent`  
  Then Plan 选 `gemini → chat` conversion，上游 chat 200，客户端收到 Gemini 形状响应；日志可见 `RouteConversion` + adapter id。

### 核心负向

- Given 请求含 tools / 非 text / thoughtSignature 续写等超出 preserves 子集  
  When Plan  
  Then fail-close（无合法边或明确 4xx），**不**静默丢字段装成功。
- Given 仅存在宽 chat 账号、不在供应商/模型窄门内  
  When generateContent  
  Then 不选该账号（避免全局选路爆炸）。

### 相关回归

- Vertex / Antigravity 的 Gemini **identity** 路径行为不变；有 identity 候选时不优先落到 chat conversion。
- 现有 `chat/messages/responses → gemini` 边与 `preservesToGemini` 不变。
- 定价仍按 client-facing model ID 挂现有 pricing SSOT；usage 映射不得系统性欠费/多扣。

### 替代路径（不建边时）

| 做法 | 说明 |
|---|---|
| 入口 remap 到 Vertex/AG 活 wire id | 仍走 Gemini identity；模型 ID/能力可能不等价 |
| 只开放 chat 契约 | 零协议债；客户端必须改协议 |
| 运营告知改活 ID | 最快止血；不解决「必须 generateContent + 必须该供应源 ID」 |

## Risks / Benefits

### 收益

1. **矩阵对称**：Gemini 不再「只进不出」，与 Plan 模型一致。
2. **解锁一类真实供给**：mapping 有 gemini-*、只会 chat 的供应源可进入 generateContent Plan，而无需伪造 native Gemini capability。
3. **客户端契约更宽**：写死 generateContent 的 SDK 不必改成 chat。
4. **运维语言统一**：adapter / preserves / 计费仍挂 client-facing model。

### 风险

1. **选路爆炸半径（最大）**  
   边是全局的。不收窄时，failover 可能把 generateContent 落到任意 chat 账号。必须：preserves + 供应商/模型窄门 + identity 优先于 conversion。

2. **语义丢保真**  
   Gemini `contents/parts`、thinking、thoughtSignature、safety、systemInstruction ↔ OpenAI messages 远于 messages↔chat。  
   preserves 过严 → 收益小；过宽 → 「200 但不像 Gemini」。

3. **能力叙事扩散**  
   账号 SupportedProtocols 仍是 chat，客户端以为 native Gemini。排障/探针/容量/sticky 必须按 conversion 解读。

4. **与现有硬门联动**  
   仅加 registry 边不够：handler Vertex 门、CloudWise 前缀门、ShapeGemini 候选门都要改；改门扩大错误承接面。

5. **计费/用量映射**  
   usage / stream 分片不完全同构 → 欠费或多扣风险。

6. **测试与回归成本**  
   preserves 正负、流式、failover、与 identity 优先级、错误码映射；比 messages↔chat 更贵。

7. **产品预期锁定**  
   边上线后客户当契约；以后想收回「generateContent 只保证 Vertex/AG」成本高。

### 何时收益大于风险

- 边与**窄门**一起上（例：仅 CloudWise ∧ 明确模型名单 ∧ text-only preserves）
- Plan **优先** Gemini identity；conversion 只作明示 fallback
- 超集 **fail-close**，不静默丢
- 存在真实刚需：必须 generateContent **且** 必须该供应源模型 ID；Vertex/AG remap 不可接受

不满足时：收益偏「矩阵好看」，风险是全局选路与假原生。

### 乔布斯向摘要（供后续拍板）

- **架构**：与现有 inbound→target bridge **同构**；差在缺边，不在新范式。
- **默认不建议宽做**；若做，按「显式 bridge 产品 + 窄门 + 能力子集」立项，不要自称「现有 SSOT 零增量」。
- **更干净的替代**往往是：入口 remap 到活 identity，或 CloudWise Gemini 只承诺 chat。

## Validation

本文档阶段：

- [x] 对照 `protocolrouter/registry.go` 确认 Gemini 相关边不对称（截至撰写时无 `gemini → chat`）
- [x] 对照 `preservesToGemini` / `preservesGeminiIdentity`（偏 text、限制 tools/reasoning）
- [ ] 实现与自动化测试（**未开始**；待评估通过后再开）
- [ ] 线上探针：`#113` + 声明模型 + generateContent（**未做**；当前预期仍为门禁/无合法边）

若后续批准实现，最低验证建议：

- registry / policy contract：新边可 Plan；超集 preserves 负向无合法边
- adapter：text hello 往返；tools/多模态 fail-close
- 与 Vertex identity 共存：同模型时不误选 chat conversion（或按明示策略）
- `./scripts/preflight.sh` + 相关 `go test -tags=unit`

## 待决问题（评估清单）

1. 是否接受「补反向边」作为产品方向，还是坚持 generateContent 仅 identity（Vertex/AG）？
2. 若接受：窄门范围（供应商 / 账号 / 模型名单）如何定？
3. 能力子集边界：tools、image、thinking、thoughtSignature 续写是否永久 out of scope？
4. conversion 是 fallback only，还是可与 identity 并列竞选？
5. 是否需要 `docs/approved/` 高风险锚点（公共协议契约 + 核心路径爆炸半径 → 倾向需要）？
