---
title: Kiro 文件工具分块兼容方案
status: approved
approved_by: "feng（本次对话：确认。）"
approved_at: 2026-10-21
---

# Kiro 文件工具分块兼容方案

用户已确认按账号显式开启分块，并接受额外模型轮数、耗时和费用的取舍。本文件是正式代码的审批基线，审批范围是实现与验证；生产启用及部署另行安排。

## 问题与边界

以 TokenKey #2764 的 `3ac329ec4c66c1829d1363ed0de871233b63d3b5` 为基线，真实 Opus 4.6 单次生成大 Write 在约 390 秒无响应体数据后 unexpected EOF；没有终止事件。该错误并非诊断程序的 600 秒请求预算，也不是它的 360 秒首输出预算：此前已经收到可见文字。

这复现了 [kiro2cc-proxy #46](https://github.com/TsinHzl/kiro2cc-proxy/issues/46) 所描述的现象，但没有证明断开方是 Kiro 服务而非中间代理，也没有捕获到 HTTP/2 RST_STREAM。不能把 240 秒当作所有账号固定阈值。

分块提示属于概率性缓解，模型可能不遵守；参数生成完毕后再检查大小不能预防此前的静默。**此方案不能承诺彻底消除 Kiro 上游断流。** 若业务要求该类调用绝不进入 Kiro，需要单独裁决供应路由策略，不能按用户提示词猜测文件大小后另造调度入口。

## 行为契约

- 使用既有 Account.Extra，布尔 `kiro_file_tool_chunking=true` 显式开启；缺失、false、非布尔值均不开启。无需 DB schema 或新 API。
- 仅请求声明 `Write`、`Edit`、`MultiEdit` 时注入。普通聊天、Read、自定义工具名不猜测映射。
- system 构造仍由 Kiro translator 拥有；提示追加在现有 Claude Code 过滤之后，避免兼容提示被整段替换。不改用户当前消息、历史工具配对或工具 schema。
- 提示要求单个调用的合计文本参数尽量控制在 6000 UTF-8 字节和 150 行以内，以较小者为准；这是试验参数，不是服务保障阈值。对于 MultiEdit 应计算全部 edits，而非每个 edit 单独计算。
- 模型提出真实工具调用 → 网关原样返回 → 客户端执行并回传真实结果 → 下一客户端请求。保持 [现有轮次边界](kiro-turn-boundary.md)。网关不执行工具、不伪造 tool_result、不自行生成续跑请求、不在已输出后重放。
- 关闭开关即回到原行为。开关改变 system，从而影响 conversation/cache 前缀；建议在新会话边界启用，不能承诺缓存完全不变。
- 没有改生产账号、Tokensea priority、供应映射、价格或路由。本原型只有临时账号副本启用该字段。

## 实现与验证

- `Account.IsKiroFileToolChunkingEnabled`：只接受 Kiro 账号的布尔开关。
- `KiroGatewayService.Forward`：读取选中账号，将选项交给 translator。
- `ClaudeToKiroWithFileToolGuidance`：单一 system 构造路径；默认 `ClaudeToKiro` 保持关闭。
- [US-070](../../.testing/user-stories/stories/US-070-kiro-file-tool-chunking.md)：常驻转换、流式/非流式、配置、轮次和失败边界测试。
- [运维说明](../kiro-file-tool-chunking-operations.md)：通过既有管理员 API 按键修改 Extra，避免替换其他配置。

Web impact: none。本次是运维账号策略，无新增用户端字段或 UI；管理端复用既有 Extra API。现有用户请求不能开启该账号策略。计费继续使用既有 owner，新增提示不另造 token/credits 计费规则；额外客户端轮次仍是各自独立请求。

## 实测依据与限制

[核实报告](../kiro-open-source-validation-20261021.md) 保留 #46 复现、完整工具循环、raw 对照和成本结果。隔离原型通过真实账号代理、canonical TLS profile 和 repository transport 发起调用，只操作合成文件。第 17–21 轮使用与正式代码相同的提示及注入位置；正式实现仅增加带平台检查的 typed getter，原始请求和工具结果语义不变。

600 行任务成功不构成所有模型、账号、MultiEdit 或客户工程的保障。分块提示曾被模型超出，仍需留存失败证据并维持既有终止/重试契约。
