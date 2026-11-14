# Kimi K3.1 Pro KVV 接口预检报告

对照基准：[Moonshot Kimi Vendor Verifier](https://github.com/MoonshotAI/Kimi-Vendor-Verifier)（KVV）  
核查对象：TokenKey 国内生产环境的 `kimi-k3.1-pro` Chat Completions  
核查时间：2026-11-14 15:50–16:23（北京时间）

本报告核对 `kimi-k3.1-pro` 的接口行为是否符合 KVV 要求的接口预检。

## 结论

整体结果：按本次实际执行的 297 项用例计算，285 项通过，通过率为 **96.0%**。

停词、工具参数 schema 抽样、历史工具调用，以及大部分流式 `prompt_tokens`，与 KVV 一致。KVV 不允许的采样参数、多数非法动态工具、没有配对的 `role=tool` 消息，都会返回 HTTP 400。同时给出 `thinking.effort` 和 `reasoning_effort` 时，以 `thinking.effort` 为准。应当拒绝的递归工具 schema 会返回 HTTP 400，并返回可定位到 schema 或 parameters 的错误信息。

具体差异和使用建议见下文。

## 覆盖范围

思考、动态工具、工具选择、回复格式、停词和工具参数 schema，每条都分别用非流式和流式各请求一次。采样参数按思考开启和关闭各测一次，不区分流式。`prompt_tokens` 按 KVV 的做法只测流式。工具参数 schema 是抽样：开启思考，输出上限用 KVV 默认的 2048，合法样例最多 12 条，再加上 KVV 始终执行的补充校验，合计 88 项。

| 项目 | 在核什么 | 结果 |
| --- | --- | --- |
| 思考、动态工具、工具选择、回复格式、停词 | KVV 现行接口套件，120 项 | 大部分与 KVV 一致，2 项函数名不一致 |
| 采样参数，以及 128 字符的工具名 | 19 项 | 18 项一致，1 项在开启思考时拒绝了 `temperature=0.6` |
| 历史工具调用缺少函数名 | 3 项 | 全部一致 |
| 流式 `prompt_tokens` | 75 项 | 66 项落在允许区间内，9 项不一致 |
| 工具参数 schema | 88 项，抽样 | 全部符合预期 |

## 已经对齐的行为

- 不传可选参数，或在不开启思考时只传 KVV 给出的默认参数，请求成功。
- `temperature` 为 `1.1`、`2.0` 或 `-0.1`，`top_p` 为 `0.8`，`presence_penalty` 或 `frequency_penalty` 为 `0.5`，以及 `n=2`，都返回 HTTP 400。思考开启和关闭时都是这个结果。
- 动态工具写在 `user` 或 `assistant` 消息上、system 消息同时有内容和动态工具、动态工具缺 `type` 或 `name`、`name` 为空、`tools` 不是数组、工具重名，都返回 HTTP 400。
- `role=tool` 且没有匹配的 `tool_call_id` 时返回 HTTP 400。
- 128 个字符的工具名可以调用。历史 `tool_calls` 缺少 `function.name` 时返回 HTTP 400。
- 空内容的 system 消息可以携带动态工具；多条这类消息、与顶层工具并存、只给动态工具并指定 `tool_choice=required`，都可以调用。
- `tool_choice` 的 `auto`、`required`、`none` 可用。非法取值和空工具列表会被拒绝。
- `response_format` 的 `text`、`json_object`、`json_schema` 可用。缺字段或类型错误会被拒绝。
- 同时发送 `thinking.effort=low` 和 `reasoning_effort=max` 时，非流式和流式都按 `low` 生效。
- 停词只截断最终正文。用例要求正文停在 `<block>no`、思考文本里保留完整的 `</block>`。非流式和流式都通过。
- 工具参数 schema 抽样在开启思考、`max_tokens=2048` 下 88 项全部符合预期。合法样例返回了能通过 schema 的参数。无法展开的递归工具 schema 返回 HTTP 400，错误文案中出现 `schema` 或 `parameters`。非流式和流式都覆盖了这四类：必填字段指向自身、两个结构互相必填、递归数组的最少项数无法满足、递归藏在分支里。

## 仍有差异

### 1. 九个请求的 `prompt_tokens` 与 KVV 不一致

下面都是流式响应里的 `usage.prompt_tokens`。KVV 允许期望值到期望值加 3。图像请求落在允许区间内。

| 请求形态 | 实际计数 | KVV 允许区间 | 相对允许区间 |
| --- | ---: | --- | --- |
| 一条助手消息，`reasoning_effort=high` | 79 | 99–102 | 少 20–23 |
| 用户和助手的短对话，`reasoning_effort=max` | 90 | 110–113 | 少 20–23 |
| 一条带思考内容的助手消息，`reasoning_effort=max` | 86 | 100–103 | 少 14–17 |
| 声明了工具，且不调用工具 | 130 | 227–230 | 少 97–100 |
| 显式指定不调用工具 | 127 | 209–212 | 少 82–85 |
| 动态工具和顶层 `tools` 同时存在 | 236 | 258–261 | 少 22–25 |
| 工具写在消息里 | 288 | 273–276 | 多 12–15 |
| 对话中途加入工具 | 310 | 295–298 | 多 12–15 |
| 连续几轮都带工具 | 319 | 304–307 | 多 12–15 |

您会看到的是用量数字，不是一份错误的回答。KVV 的期望值来自 Moonshot 标定的分词常数。TokenKey 返回的是这次请求计入的 `prompt_tokens`。按 `prompt_tokens` 展示用量或计费时，这九类请求会和 KVV 的常数有差。

### 2. 函数名里带 `@` 的动态工具没有被拒绝

KVV 要求函数名 `bad@name` 返回 HTTP 400。非流式和流式都返回了 HTTP 200，并按这个名字发起了工具调用。

这不影响名字合法的工具。缺字段、空名字、重名、写错位置的动态工具仍然返回 HTTP 400。请不要依赖「名字里有 `@` 就会被拒绝」。

### 3. 开启思考时，`temperature=0.6` 被拒绝

本次 KVV 接口预检用例在思考开启时使用 `temperature=0.6`，并期望请求成功。这次返回 HTTP 400，错误说明这个模型只接受 `temperature` 为 `1`。

不开启思考时，`0.6` 可以通过。不传 `temperature`、只开启思考，请求也成功。`1.1`、`2.0`、`-0.1` 以及其他被 KVV 禁止的采样值，思考开和关都会拒绝。

如果您在开启思考时沿用 `0.6`，请改成不传 `temperature`，或改成 `1`。
