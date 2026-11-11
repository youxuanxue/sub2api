---
title: newapi OpenAI 渠道的 image edits 转发
status: pending
approved_by: pending
created: 2026-11-11
authors: [agent]
risk: high
---

# newapi OpenAI 渠道的 image edits 转发

## 决策

`POST /v1/images/edits` 在执行账号是 newapi、且渠道类型为 OpenAI（`channel_type=1`）、并配置了 `base_url` 时，走现有 OpenAI Images API-key 转发。上游地址是该账号的 `base_url` 加上 `/v1/images/edits`，鉴权使用账号自己的 API key。

其它 newapi 渠道仍返回 HTTP 404 `The image edits API is only available for OpenAI platform groups`。原生 OpenAI 与 Grok 的编辑路径不变。`/v1/images/generations` 的 newapi bridge 不变。

## 原因

OpenAI 分组里可以选中 newapi 账号。执行平台随之变成 `newapi`，编辑入口原先只放行 `openai` 和 `grok`，请求在进上游之前被本地拒绝。OpenAI 渠道的编辑协议与原生 API-key 转发相同，不需要为 Ali、VolcEngine 等渠道打开统一编辑入口。

## Owner

- `backend/internal/server/routes/gateway_tk_openai_compat_handlers.go`：`tkOpenAIImageEditsDispatch`
- `backend/internal/service/openai_images_newapi_edits_tk.go`：账号判定与 base URL
