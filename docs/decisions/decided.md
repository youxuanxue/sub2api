# 已决定

## 2026-11-14 Kiro 缓存拆分比例

原话：「1、60% 记成缓存读取，另外 40% 仍按输入价。改为 20% 按缓存读取，另外 80% 仍按输入价。改为 10% 按缓存读取，另外 90% 仍按输入价。2、新写进去的后缀满 1024 token 时，50% 记成缓存写入，另外 50% 仍按输入价。改为 新写进去的后缀全部按缓存计入，按未知 TTL 的 1 小时价结算。」

适用范围：Kiro 本地前缀指纹计费。匹配前缀仍要满 1024 token 才拆；拆开后 10% 为 `cache_read`，90% 留在输入价。满 1024 token 的新后缀全部记 `cache_creation`，不写 5m/1h 明细，因此沿用未知 TTL 的 1 小时写入价。不足 1024 token 的新后缀仍按输入价。只影响新结算。

## 2026-11-14 Kimi 两套 effort 与递归 schema 文案

原话：「1. 两套 effort 同时出现，同意建议。2. 8 项递归 schema 建议改下文案。」

适用范围：`kimi-k3` 家族的 effort。`thinking.effort` 存在且思考未关闭时，写入供应实际采用的 `reasoning_effort`，覆盖同时出现的 `reasoning_effort`。只带 `reasoning_effort` 的请求不变。递归工具 schema 的通用 400 句，仅在 `kimi-*` 且请求带了工具 `parameters` 时，给客户文案补上 `parameters schema`。状态码仍是 400。不停词适配，不改 `prompt_tokens`。

## 2026-11-13 Gemini 截断只计已经写出的内容

原话：「改为：1. 还没写出可用内容、并且已经换号的截断不再计数。2. 已经把正文、工具或媒体写给客户端的截断仍计数，但改成 5 分钟内满 3 次才冷却。3. 冷却档位不变。」以及「继续从 5 分钟收窄到 1 分钟吧。」

适用范围：原生流截断和 `unexpected EOF`。空前缀换号不计入阶梯。正文、工具或媒体已经写给客户端后，1 分钟内第 3 次才冷却，时长仍是 `anthropicCooldownTierLadder`（30 秒、2 分钟、10 分钟）。冷却未结束前不升档。`INSUFFICIENT_G1_CREDITS` 仍立刻临时停调度 30 分钟。

## 2026-11-13 Gemini 4 Hydrogen 裸名映射 medium

原话：「把能访问的这些账号的 model_mapping 加上 gemini-4-hydrogen-low、-medium、-high、-xhigh，裸名映射到 -medium。」

适用范围：已实测能访问 Hydrogen 的 Antigravity 账号。四档 token 单价相同。官方默认思考档是 high，裸名仍锁 medium。档位 wire 不上公开目录。不能访问的账号不加这组映射。

## 2026-11-12 Gemini 截断冷却改走已有阶梯

原话：「建议改一下：同一账号 15 分钟内第 3 次原生流截断或 unexpected EOF，临时移出调度 30 分钟。这个有已有的阶梯冷却策略，可以复用吗？直接冷却30分钟太狠了，对我们的池子不友好。INSUFFICIENT_G1_CREDITS 立刻停止调度30分钟。」

适用范围：原生流截断和 `unexpected EOF` 仍是 15 分钟内第 3 次才冷却，时长复用 `anthropicCooldownTierLadder`（30 秒、2 分钟、10 分钟）。冷却未结束前不升档。`INSUFFICIENT_G1_CREDITS` 立刻临时停调度 30 分钟。不永久禁用。

## 2026-11-12 无供给模型 404，支持数为 0 则 400

原话：「同意2的修复建议：建议：没有任何可调度账号能提供的模型，返回 404，不要返回可重试的 429。gemini-4-argon 这 11 次先看那把钥匙的 capacity 诊断（account_total、supported、ready、拒绝原因）。诊断现在只在应用日志里，不在 ops_error_logs。supported=0 就应按不支持的请求返回 400；ready=0 才是真的容量问题，去补这把钥匙的组，不要改 Plan。」

适用范围：万能钥匙选号。没有任何可调度账号承认的模型返回 404。容量诊断 `supported=0` 返回 400 `unsupported_request`，不返回可重试 429。`supported>0` 且 `ready=0` 仍是容量 429。不改协议 Plan。容量诊断写入 ops 内部明细。

## 2026-11-12 Gemini 截断熔断与原生请求体校验

原话：「第 1 个 和 第 3 个，同意建议，请修复。」

适用范围：同一 Antigravity/Gemini 账号在短窗口内反复出现原生流截断或 `unexpected EOF` 时临时移出调度；`INSUFFICIENT_G1_CREDITS` 单独临时停用。原生 `generateContent` / `streamGenerateContent` 在缺少 `contents`，或顶层出现 `messages`、`max_tokens`、`input`、`store` 时本地 400。Antigravity 拒绝 `gs://` 和 `http(s)` `fileUri`，只接受 `inlineData`。不在原生路径上把这些请求改写成 Chat。

## 2026-11-12 手动停调度不进 Plan

原话：「不对，schedulable=false 不应该进 plan，池子不足会是其他的临时不可调度、限流等。」以及「同意。请修复。」

适用范围：候选选号的协议 Plan。`Schedulable=false` 在 Plan 之前排除。临时不可调度、限流、过载、配额仍进入 Plan，只有这些合法路径时仍是容量 429。

## 2026-11-12 Gemini 协议转换按 Chat 直接路径放宽选号

原话：「同意。按照建议方案从 1-4 逐个实现。」

适用范围：Chat→Gemini 直接转换；Messages→Gemini 保留图片、thought part 和 tool_choice；Responses 先桥到 Chat 再转 Gemini；原生 Gemini 带图仍走 identity。不给 Antigravity OAuth 声明原生 Chat/Responses/Messages。远程图片 URL、strict、关闭并行工具、内置工具与 function tools 混放仍然拒绝。

## 2026-11-12 Gemini 4 Argon / Carbon 默认映射 medium

原话：「如果 gemini-4-carbon-medium 和 gemini-4-carbon-high 都是一样的定价的话，为了提升用户体验，我们应该把 gemini-4-carbon/argon 都默认映射到 -medium。」

适用范围：Antigravity 上 `gemini-4-argon` 与 `gemini-4-carbon` 的裸名默认 wire。Flash 家族仍锁 high。low/medium/high 的 token 单价相同；未指定 thinking 时走 medium，显式档位仍按请求选择。

## 2026-11-12 去掉 typed tokensea relay 地板

原话：「现在没有 tokensea 账号啊，tokensea 是通过供应源及其受管账号提供服务的，是不是有遗留的代码导致了错判啊？」以及「同意。继续。去掉之后需要实测下，确保不会造成服务影响。」

适用范围：删除 openai/anthropic 平台上的 tokensea relay 识别、地板、目录和调度特例。`agent.tokensea.ai` 的请求体剥离（`context_management`、`cache_control.scope`）按 host 保留，覆盖供应源受管的 newapi 账号。不改现网账号映射，不把 Carbon 激活写进这次清理。
