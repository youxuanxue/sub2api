---
title: GPT Image OAuth native Images cutover
status: approved
approved_by: "user (本会话：同意切到原生的生图上)"
approved_at: 2026-10-26
created: 2026-10-26
authors: [codex]
risk: high
amends: ["docs/approved/gpt-image-strict-canvas-postprocess.md"]
related_evidence:
  - "/tmp/image-native-ab-20261026T102938Z/report.md"
  - "/tmp/image-native-postchange-20261026T111346Z/report.md"
---

# GPT Image OAuth 原生 Images 切换契约

本文件 amend `gpt-image-strict-canvas-postprocess.md` 中“OAuth / Setup-Token 一律走
Responses”的传输决策。共享的严格画布、格式、计费和错误分类契约继续有效；本文件对
上游传输及其验收拥有更晚的决策。

## 决策

对 `gpt-image-1.5`、`gpt-image-2` 和已登记的 `gpt-image-2.5-*` family，OAuth / Setup-Token
默认调用：

- `POST /backend-api/codex/images/generations`
- `POST /backend-api/codex/images/edits`

请求使用原生 Images JSON 形状，保留已解析的 `model`、`prompt`、`size`、`quality`、
`background`、`output_format`、`input_fidelity`、`n`、压缩、partial images 和输入图片。
未经解析的客户端字段不转发。

如果原生 endpoint 返回 404 或 405，同一请求只回退一次到 Responses；其它错误保持原有
错误分类与账号处理。

## 客户契约 owner

- 尺寸：`applyOpenAIImagesClientFidelityPostprocess` 与严格 canvas owner 决定最终像素；
  原生 prompt 追加明确宽高和比例提示，只用于提高上游构图遵循。
- 格式：显式 PNG/JPEG/WebP 由本地重编码和魔数校验兜底；JPEG 不保留 alpha。
- 透明度：PNG/WebP 允许上游 alpha 或透明 padding；无法把已经生成的不透明背景自动
  抠成透明。
- 质量：原生 `quality` 字段和 prompt guidance 传达请求意图；响应中的 quality 标签
  仍是上游元数据，不升级为网关质量保证。
- usage/billing：复用原生 Images usage parser 和现有 billing owner。

## 验收与回滚

发布前必须通过定向与完整服务测试、preflight、xj-review，并完成 generations、edits、
format/size、stream、`n>1`、usage/billing、影子账号和 404/405 fallback 验收。

生产发布后执行 immediate 与 delayed smoke，观察原生成功率、完成延迟、格式/尺寸和回退
计数。出现原生错误率或客户质量回归时，将 `usesCodexDirectImages` 恢复为旧的
Responses 默认值并重新发布；Responses 路径保留作为即时代码级回退。
