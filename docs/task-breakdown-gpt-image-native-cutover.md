# GPT Image OAuth 原生生图切换拆解

## 目标

将 OAuth / Setup-Token 的 GPT Image family 默认上游从 Responses + `image_generation`
切换到 `/backend-api/codex/images/generations` 与 `/images/edits`，降低客户感知延迟，
同时保留网关已承诺的尺寸、格式和响应兼容行为。

实测同输入矩阵显示原生路径中位完成时间约 12.8 秒，Responses 约 29.2 秒；配对样本
原生路径中位节省约 16.4 秒。原生与 Responses 在主体、文字、构图和编辑边界上未发现
系统性差异。尺寸遵循由本地严格画布后处理兜底，原生请求额外携带明确尺寸提示。

## 做什么

1. GPT Image family 默认使用原生 generations / edits endpoint。
2. 继续复用共享 prompt scaffold、质量提示、尺寸提示、比例 marker 和本地严格画布、
   格式后处理。
3. 原生返回解析、usage 归集、URL / base64、编辑图片、`n`、stream 和影子账号路径
   继续走已有 owner。
4. 原生返回 404 或 405 时，对同一请求回退 Responses。
5. 增加路由、payload、尺寸、质量提示、usage、编辑和回退测试，并用真实 smoke 验收。

## 不做什么

- 不删除 Responses 实现；它仍是能力缺失时的回退路径。
- 不承诺把已经生成的不透明背景抠成透明；透明背景只在上游返回 alpha 或本地透明
  padding 可兑现时成立，JPEG 继续强制不透明。
- 不把上游 `quality` 响应标签当作客户质量契约；标签由上游决定，prompt 和本地后处理
  负责请求意图及输出契约。
- 不修改 GPT Image 模型目录、计费价格或账号选择策略。
- 不在本次变更中引入新的超分模型、队列或异步 API。

## 依赖与顺序

1. 原生 payload / response builder 与单元测试。
2. 高风险契约 amendment 与回滚开关确认。
3. 完整服务测试、preflight、xj-review。
4. 合并后发布检查、生产部署和 immediate / delayed smoke。

## 验收

- generations 与 edits 均命中原生 endpoint，404/405 可回退 Responses。
- 显式 `size=WxH` 的最终图片为请求尺寸；显式 `output_format` 的魔数和声明一致。
- PNG/WebP 的透明 padding 行为、JPEG 不透明行为保持既有契约。
- prompt、质量、比例 marker、编辑输入、`n`、stream、usage/billing 回归通过。
- 生产 smoke 记录原生端点、成功率、首字节/完成延迟和回退计数；异常时恢复旧开关并重新发布。
