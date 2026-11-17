# Spec delta: AG Gemini native thinkingBudget clamp + argon maxOutputTokens hard cap

## Background

Prod（`rt@tk.com`）对 Antigravity `gemini-4-argon` 走 `/v1beta/models` 原生路径时，thinking 打满目录上限 65536，并出现上游 400：`thinking_budget must be in the range [-1, 65535]`。根因是客户端传了非法/超大 `thinkingBudget`，且未设 `maxOutputTokens` 时上游默认输出上限过高；与缺 `project_id` 无关。

## Delta

### ADDED

- ForwardGemini 入口对用户已提供的 `generationConfig.thinkingConfig.thinkingBudget` 做合法域规范化：
  - 保留 `-1`（动态）
  - budget 模型：`<-1` → `-1`，`>65535` → `65535`
  - thinkingLevel 模型（含 `gemini-4-argon*`）：非法/超大 budget **丢弃**（不经 `budget>8192→high`）；合法 budget 改写为 `thinkingLevel` 并删除 budget；已有 level 时丢弃 budget
- `gemini-4-argon*` 代码 SSOT 硬顶 `max_output_tokens_cap=8192`：
  - 客户端显式更大 → 钳到 8192
  - 未设 `maxOutputTokens` 且存在可操作 thinkingConfig → 注入 8192（可关常量）
- 改写打脱敏 slog：`antigravity_gemini_native_thinking_rewritten`（action/from/to，无 body）

### MODIFIED

- `ForwardGemini`：在 `resolveGeminiThinkingVariant` 之前调用规范化

### REMOVED

- 无。Claude→AG `TransformClaudeToGemini` 的 64000 默认保持不动。

## Scenarios

| 类型 | 场景 | 期望 |
|---|---|---|
| 正向 | argon + `thinkingBudget:4000` | → `thinkingLevel=medium`，注入 `maxOutputTokens=8192` |
| 正向 | argon + `thinkingLevel=medium` 无 max | 注入 `maxOutputTokens=8192` |
| 负向 | argon + `thinkingBudget:65536` | 丢弃 budget，不写 high；不助推 -high 变体 |
| 负向 | argon + `maxOutputTokens:65536` | 钳到 8192 |
| 回归 | `gemini-2.5-flash` + `thinkingBudget:70000` | 钳到 65535，无 level 改写、无 cap 注入 |
| 回归 | argon 无 thinking | 不注入 maxOutputTokens |

## Validation

```bash
cd backend && go test -tags=unit ./internal/service/ -count=1 \
  -run 'TestNormalizeAntigravityNativeGeminiRequest_|TestAntigravityGeminiMaxOutputTokensCapForModel|TestIllegalArgonBudgetDoesNotResolveToHighVariant'
```
