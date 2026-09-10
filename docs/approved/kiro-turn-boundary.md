---
title: Kiro client-owned turn boundary
status: approved
approved_by: "feng (本次对话：同意。请继续。)"
approved_at: 2026-09-10
---

# Kiro 当前轮次与任务完成的边界

用户批准移除网关强制续跑，修复并验证后提交独立 PR；不部署、不修改线上配置。
本决策替代旧 completion-continuity 设计中的私有工具、guard 与隐藏续跑。

## 已确认的根因

1.8.214 在 Claude Code 文本响应正常 END_TURN 后，因缺少私有完成信号，
把真实用户指令移到历史，再追加“Continue the same task now”。
这会在用户只问状态或禁止工具时，生成额外 Read/Edit，并与前一答复合并。
完整帧、CRC 和显式 END_TURN 均已观察到；该案例不是 stop reason 丢失。

2026-09-09 真实上游对照共 23 次请求：保留协议的 8 个首轮中，7 次正常结束，
1 次首轮就产生 Edit；7 次隐藏续跑中 4 次产生普通工具、3 次产生 blocked。
仅移除协议的 8 个对照首轮均无工具。样本有限，不证明消除所有模型幻觉。
本机真实 Forward 回放独立复现了两次上游调用合并成 NO + Read 的行为。

真实 Kiro CLI 2.21.1 的受控事件流与本地工具执行采证显示：保留结构化工具历史，
正常结束即交还控制权，没有上述私有完成工具。二进制/source strings 只辅助定位，
不作为真实 wire 证明。当前 CLI 账号无法成功调用真实上游，不能混称实测。

## 契约

- 每次客户端请求只代表一个模型轮次；合法 END_TURN 原样结束。
- 不注入业务完成 guard、私有工具或网关生成的新 user 指令；不吞同名客户端工具。
- 保留正常工具调用、失败结果、历史配对、thinking side channel、估算用量及缓存归因。
- 保留发送可见内容前的传输重试。已经发送内容后不能重放当前请求。
- 保留帧边界、工具输入、终止元数据及截断流校验；max_tokens/refusal 等继续保真。
- END_TURN 不是业务任务完成证明。模型仅输出进度而未执行工具时，
  网关不能假造完成，也不能自行继续业务。后续执行由客户端 agent 的任务循环负责。
- 共享 OpenAI-compatible Messages guard 不属于本次 Kiro 修复范围。

## 验证与限制

回归锚点见 US-041：复现先红后绿、流式/非流式轮次边界、客户端工具历史、
失败状态、正常工具循环及已有异常流用例。真实上游工具循环只修改本机临时 fixture，
不修改生产业务文件；这属于 API/工具集成实测，不是 UI e2e。

2026-09-10 本机实测使用本分支 ClaudeToKiro 构造每一轮请求，edge 仅直调上游，
工具由本机限制路径的 runner 执行。Opus 5 用 4 轮、Opus 4.8 用 5 轮完成：
Read 缺失文件（真实失败）→ Read 实际文件 → Edit 随机值 → Verify 实际运行
bash -n 和 SHA256 → 最终答复包含匹配的哈希。9 次上游调用均 HTTP 200，
CRC 和工具输入完整，最终 END_TURN 无额外请求。
探针使用 Python TLS；该结果不等同于完整生产网关/uTLS 或真实 Claude Code UI 实测。
本地证据：`/tmp/kiro-boundary-live-results.json` 与逐轮采证
`/tmp/kiro-boundary-live-loop.log`。

user_id=16 原始投诉的两个会话缺少完整逐轮上游证据，不能声称全部精确复现或根治。
需要发版后继续验证真实客户端，尤其模型自身提前总结的发生率。
