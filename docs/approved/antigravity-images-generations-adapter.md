---
title: Antigravity OpenAI Images generations adapter
status: approved
approved_by: "feng：本会话明确要求“补一层协议。继续。”（2026-09-30）；授权实现，不等同于合并或部署授权"
created: 2026-09-30
---

# Antigravity 的 OpenAI Images 接口适配

## 目标与契约

现有 Antigravity OAuth 和 prod→edge relay 已能通过 Gemini 原生请求生图，但
`POST /v1/images/generations` 在路由层返回 404。本次为这一入口增加协议适配，
让 OpenAI Images 客户端得到标准 `created` + `data[].b64_json`。

实现复用既有原生 Gemini generation handler、账号授权、`protocolrouter.Plan`、
调度、并发、供应源凭据恢复和计费生命周期。图片只生成一次、计量一次。
本会话批准实施该适配；不包含更改线上账号、优先级或部署。

输入为 JSON：

| 参数 | 契约 |
| --- | --- |
| model / prompt | 必填字符串；model 必须是获授权且 Plan 可执行的 Gemini 图片型号/别名；prompt 原文保留 |
| n | 省略或 1；其他值 400，不循环生成多张 |
| size | 省略/auto 默认 2K；支持 1K/2K/4K；1024x1024、2048x2048、4096x4096 对应方图档位 |
| aspect_ratio | 可选构图比例，默认 1:1，使用共享 Gemini 比例表；与显式方形 size 冲突则 400 |
| response_format | 省略或 b64_json；url 返回 400，不把 data URI 冒充托管图片 URL |
| stream | 省略或 false；true 返回 400，原生/Chat 等已有入口继续提供 SSE |
| user | 可选字符串客户端标记，不传给 Google，不改变鉴权身份 |

非方形 WxH 不被近似成另一尺寸；客户端用 aspect_ratio + size 档位表达。
quality、background、output_format、mask、输入图、其他未知字段返回 400，
不静默丢弃。`/images/edits` 不在本次范围。Gemini Web、普通 Gemini API/Vertex
账号不因这项适配获得 Images 候选资格，原有其他供应商 Images 入口维持原 owner。

错误按 OpenAI error envelope 返回，保留 HTTP 状态和 Retry-After。成功结果须为
正常终止的一张合法 MIME/base64 图片；空图片、未终止和超出 32 MiB facade
响应缓冲上限返回 502。生成输出已发生后不因协议转换错误重放生图。

## 请求和执行

1. 候选准入通过共享转换器验证 AG 参数，不支持的参数不能绑定 AG；其他既有 Images 供应商保留原协议。无可执行路径时，HTTP middleware 对非法 AG 参数返回 400。
2. `UniversalRoutingResolver.WithRequest` 使用同一转换器构造不可变 Gemini 请求。
3. AG 的 `CandidateRequest` 必须持有合法原生 Gemini Plan；同名模型的既有 newapi Images 路径保留自己的协议，并继续执行
   图片权限、组范围、映射、余额与可用性检查。
4. 实际选择的账号决定 handler。Images facade 将同一 native body 交给既有 Gemini
   handler，保留原始 URL，因此日志/用量入口仍为 `/v1/images/generations`。
5. 非流式响应在边界缓冲后转换为 Images envelope；既有 handler 在提交用量前完成转换校验。空图、坏 base64、多图或未完成响应返回 502，不计费、不重放。
6. 只有图片映射的 AG 账号也必须具有有效的原生 Gemini protocol capability，不能走非治理旁路。

## Web 和发现

现有 capabilities 增加经实际候选准入证明的 Images profile：单张、构图比例、无输入图。
Studio/Quickstart 的 Gemini 默认仍优先既有 Chat/native profile，不增加并行状态机或
新的参数控件。公开 API 说明给出这个可选入口的示例和参数边界。

候选 owner 清单仍只由 [Candidate Eligibility SSOT](candidate-eligibility-ssot.md)
的 Implementation/Owners 表维护。

## 验证

- 请求转换：默认 2K、显式尺寸/比例、prompt 保留、拒绝无法兑现的参数。
- Direct/Universal：原生 Plan 与执行使用相同 body/digest；不能借用无权组、关闭图片的组、
  Web 账号或不支持原生 Gemini 的账号。
- 响应：标准 b64_json、错误状态和 Retry-After、空/坏图片、缓冲超限。
- 集成：mock 上游捕获 Gemini 请求，返回真实可解码图片，验证只请求一次、图片数/尺寸和
  最终 OpenAI Images envelope；保留已有 OpenAI/Grok/newapi 路由回归。
- 本地实现/测试不代表线上部署后验收；后续上线需补公开 Key 的实际请求与用量关联。

## 调用示例

```bash
curl "$TOKENKEY_BASE_URL/v1/images/generations" \
  -H "Authorization: Bearer $TOKENKEY_API_KEY" \
  -H 'Content-Type: application/json' \
  -d '{"model":"nano-2","prompt":"一只蓝色陶瓷杯，产品摄影","size":"2K","aspect_ratio":"16:9","n":1,"response_format":"b64_json"}'
```

`TOKENKEY_BASE_URL` 填网关 origin（不带 `/v1`），使用有图片权限的 TokenKey Key。
客户端读取 `data[0].b64_json` 并 Base64 解码为图片文件；不依赖网页 Cookie，
不要求客户端获取或保存 Google OAuth token。

AG 的 size 是分辨率档位，aspect_ratio 是原生构图参数；不能承诺每个模型输出固定像素。
此前已测通的 native/Chat/Responses/Messages 仍可用于图生图与流式调用。
本协议层只提供单张、非流式文生图。

## 本地验证结果（2026-09-30）

以下测试在当前工作树通过，覆盖 OAuth 与 relay handler、参数/权限边界、真实 PNG
解码、计量归属与未交付结果不计费；mock 上游不代表 Google 线上验证。

```bash
cd backend
go test -p 2 -tags=unit ./internal/pkg/apicompat ./internal/engine/protocolrouter ./internal/service ./internal/handler ./internal/server/middleware ./internal/server/routes -run 'Test(ImagesToGemini|GeminiGenerationToImages|Gemini|Antigravity|WrapNativeGeminiRequest|Candidate|GlobalCandidate|Universal|Protocol|TkOpenAI|US057_Gemini|US059|ImageCapability)' -count=1
```

Gateway sentinel 校验、JSON 格式校验和 `git diff --check` 通过。
验收场景与测试映射见 [US-059](../../.testing/user-stories/stories/US-059-antigravity-images-generations.md)。
当前改动尚未部署，线上 `/v1/images/generations` 的旧 404 结论尚未由新版本实测更新。
