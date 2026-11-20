---
title: Apex 域名阶段二 — tokenkey.dev 人类入口 / api.* 机器入口
status: approved
approved_by: feng (对话审批 2026-07-28)
authors: [agent]
created: 2026-07-27
related_prs: ["1477"]
revised: 2026-11-20（删除 rollout 执行史；人类入口以 CallModel 门面为准）
---

# Apex / api.* 域名分工

## 决策

**机器入口钉 `api.tokenkey.dev`（OAuth / 支付 webhook 不改 host）。**
人类商业入口以 [`design-callmodel-product-facade.md`](design-callmodel-product-facade.md) 为准
（`callmodel.io` / `api.callmodel.io`）；本文件不再拥有 `frontend_url` / 面向用户的 `api_base_url`。

| 受众 | Canonical host | 用途 |
| --- | --- | --- |
| API 客户端 / CLI / SDK | `https://api.tokenkey.dev` | `/v1/*`、gateway 别名、`api_base_url`（机器） |
| 第三方服务端回调 | `https://api.tokenkey.dev` | OAuth callback、支付 webhook |
| 浏览器 / 营销 | 见 CallModel 门面 | 不在本文件维护 |

## Caddy `@machine` allowlist（仍有效）

`api.tokenkey.dev` 用 allowlist，其它 path 301 到人类入口（现网人类入口见门面文档）：

- `/health*`、`/v1/*`、`/v1beta/*`
- `/api/v1/auth/oauth/*/callback`、`/api/v1/auth/oauth/*/*/callback`
- `/api/v1/payment/webhook/*`、`/api/event_logging/batch`
- `/backend-api/codex/*`、`/antigravity/*`
- `/responses*`、`/chat/completions*`、`/embeddings*`、`/models`
- `/images/*`、`/messages/count_tokens`、`/alpha/search`、`/videos*`、`/video/*`

新增 gateway 前缀时扩 allowlist；默认其它 path 不服务人类 SPA。

阶段二 rollout 执行步骤已删除。
