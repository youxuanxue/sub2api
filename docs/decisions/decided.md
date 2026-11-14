# 已决定

## 2026-11-14 Antigravity Claude 进 claude 组

原话：「只试点 ultra，裸名映射到 -medium。」「268 / 291 / 294，把他们做和 ultra 一样的改动。」

适用范围：prod 可调度 Ultra（233/244/292/529）与已 advertise Claude 5.5 的 Pro（268/291/294）。裸名 → `-medium`，档位透传；`mixed_scheduling=true`；挂 `claude`+`Google-Antigravity`。其余账号与 SSOT floor 不动。

## 2026-11-14 Kiro 缓存拆分比例

原话：「改为 10% 按缓存读取，另外 90% 仍按输入价。」「新写进去的后缀全部按缓存计入，按未知 TTL 的 1 小时价结算。」

适用范围：Kiro 本地前缀指纹。满 1024 才拆：读侧 10% `cache_read` / 90% 输入价；新后缀满 1024 全记 `cache_creation`（未知 TTL→1h 写入价）。只影响新结算。

## 2026-11-14 Kimi 两套 effort 与递归 schema 文案

原话：「1. 两套 effort 同时出现，同意建议。2. 8 项递归 schema 建议改下文案。」

适用范围：`kimi-k3`：有 `thinking.effort` 且思考未关时，以它覆盖 `reasoning_effort`。`kimi-*` 带工具 `parameters` 的递归 schema 400 文案补 `parameters schema`。不停词、不改 `prompt_tokens`。

## 2026-11-13 Gemini 截断冷却（最终）

原话：「空前缀换号不计数；已写出正文/工具/媒体的截断，1 分钟内满 3 次才冷却；档位不变。」

适用范围：原生流截断与 `unexpected EOF`。冷却仍走 `anthropicCooldownTierLadder`。`INSUFFICIENT_G1_CREDITS` 立刻停调度 30 分钟。取代此前 15 分钟窗口方案。

## 2026-11-13 Gemini 4 Hydrogen 裸名映射 medium

原话：「能访问的账号加上 hydrogen low/medium/high/xhigh，裸名映射到 -medium。」

适用范围：已实测可访问 Hydrogen 的 Antigravity 账号。档位 wire 不上公开目录；不能访问的不加。

## 2026-11-12 无供给模型 404，supported=0 则 400

原话：「没有任何可调度账号能提供的模型返回 404，不要可重试 429。」「supported=0 → 400；ready=0 才是容量问题。」

适用范围：万能钥匙选号。`supported=0` → 400 `unsupported_request`；`supported>0` 且 `ready=0` 仍 429。不改协议 Plan。

## 2026-11-12 Gemini 截断熔断与原生请求体校验

原话：「第 1 个 和 第 3 个，同意建议，请修复。」

适用范围：短窗口反复原生截断/`unexpected EOF` 临时移出调度；`INSUFFICIENT_G1_CREDITS` 单独停用。缺 `contents` 或顶层误带 `messages`/`max_tokens`/`input`/`store` → 本地 400。Antigravity 只接受 `inlineData`，拒绝 `gs://` 与 http(s) `fileUri`；不改写成 Chat。

## 2026-11-12 手动停调度不进 Plan

原话：「schedulable=false 不应该进 plan。」「同意。请修复。」

适用范围：`Schedulable=false` 在 Plan 前排除。临时不可调度/限流/过载/配额仍进 Plan，仅这些路径时为容量 429。

## 2026-11-12 Gemini 协议转换按 Chat 直接路径放宽选号

原话：「同意。按照建议方案从 1-4 逐个实现。」

适用范围：Chat→Gemini 直转；Messages→Gemini 保留图/thought/tool_choice；Responses 先桥 Chat 再转；原生带图仍 identity。Antigravity OAuth 不声明原生 Chat/Responses/Messages。远程图 URL、strict、关并行、内置+function 混放仍拒。

## 2026-11-12 Gemini 4 Argon / Carbon 默认 medium

原话：「carbon/argon 默认映射到 -medium。」

适用范围：Antigravity 上 argon/carbon 裸名默认 medium；Flash 仍锁 high。显式档位按请求。

## 2026-11-12 去掉 typed tokensea relay 地板

原话：「没有 tokensea 账号，是供应源受管的。」「同意。继续。」

适用范围：删 openai/anthropic 上 tokensea relay 识别/地板/目录/调度特例。`agent.tokensea.ai` 请求体剥离按 host 保留。不改现网映射。
