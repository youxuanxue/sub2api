# 已决定

## 2026-11-12 手动停调度不进 Plan

原话：「不对，schedulable=false 不应该进 plan，池子不足会是其他的临时不可调度、限流等。」以及「同意。请修复。」

适用范围：候选选号的协议 Plan。`Schedulable=false` 在 Plan 之前排除。临时不可调度、限流、过载、配额仍进入 Plan，只有这些合法路径时仍是容量 429。

## 2026-11-12 Gemini 4 Argon / Carbon 默认映射 medium

原话：「如果 gemini-4-carbon-medium 和 gemini-4-carbon-high 都是一样的定价的话，为了提升用户体验，我们应该把 gemini-4-carbon/argon 都默认映射到 -medium。」

适用范围：Antigravity 上 `gemini-4-argon` 与 `gemini-4-carbon` 的裸名默认 wire。Flash 家族仍锁 high。low/medium/high 的 token 单价相同；未指定 thinking 时走 medium，显式档位仍按请求选择。

## 2026-11-12 去掉 typed tokensea relay 地板

原话：「现在没有 tokensea 账号啊，tokensea 是通过供应源及其受管账号提供服务的，是不是有遗留的代码导致了错判啊？」以及「同意。继续。去掉之后需要实测下，确保不会造成服务影响。」

适用范围：删除 openai/anthropic 平台上的 tokensea relay 识别、地板、目录和调度特例。`agent.tokensea.ai` 的请求体剥离（`context_management`、`cache_control.scope`）按 host 保留，覆盖供应源受管的 newapi 账号。不改现网账号映射，不把 Carbon 激活写进这次清理。
