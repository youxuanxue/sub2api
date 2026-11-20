---
title: OpenAI-compat Messages 自动压缩策略（账号/分组）
status: shipped
approved_by: xuejiao (release gate approval 2026-05-07)
approved_at: 2026-05-07
authors: [agent]
created: 2026-05-07
related_prs: []
related_commits: [aa68310a, 08d2ebc8, f9cfe5c5]
related_stories: [US-034]
revised: 2026-11-20（删背景/验证矩阵散文；保留策略契约与 owners）
---

# OpenAI-compat Messages 自动压缩策略（账号/分组）

`status: shipped`。US-034 仍为 InTest。验收以 Story + Linked Tests 为准。

## 0. TL;DR

OpenAI-compat `/v1/messages`：**账号优先、分组兜底**的自动压缩。

- 优先级：`account.extra > group`
- 仅当策略显式启用且 `input_tokens` 达阈值时触发
- 无配置 = 不压缩（默认不变）
- OpenAI OAuth/Plus/Codex 显式启用后共用裁剪引擎的 anchor+tail profile
- `previous_response_id` 回退路径结构化观测：`not_found` vs `unsupported`

## 3. 范围 / 非目标

范围：Messages 路径；分组 schema 策略字段；账号 `extra` 覆盖；回退观测。

非目标：全局 config；改变 OAuth 默认未配置行为；会话级 continuation 映射。

## 5. 策略

- 解析：`account.extra` 覆盖 group；显式关闭可压过 group 启用。
- 触发：启用且 `input_tokens` ≥ 阈值。
- 裁剪：单一参数化 replay compaction engine；APIKey = tail-only；OAuth =
  保留 system、首轮 anchor、尾部窗口与 tool 边界的 anchor+tail。

## 6. 回退观测

- `not_found`：删除 response_id 后重试
- `unsupported`：禁用 continuation 后重试  
  （仅增强观测字段，不改状态机语义）

## 7. Owners

| 行为 | Owner |
| --- | --- |
| 主注入 | `openai_gateway_messages.go` |
| 策略 companion | `openai_messages_compaction_tk.go` |
| 分组模型 | `group.go` + admin/repo/cache/DTO 透传 |
| 覆写防护 | `scripts/sentinels/engine-facade.json` |

## 8. 风险控制

压缩过度 → 单一引擎 + profile 边界；continuation 回归 → 只加观测；配置断链 →
admin/repo/cache/DTO 回归测试。
