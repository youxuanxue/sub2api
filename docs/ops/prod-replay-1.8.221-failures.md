# 1.8.221 prod replay 失败根因审计

审计对象为 [replay run 34673499862](https://github.com/youxuanxue/sub2api/actions/runs/34673499862)，2026-09-12 04:38:50–04:53:14 UTC（北京时间 12:38:50–12:53:14）。原始回执保持 red：44 条执行记录，33 条通过、11 条失败；另有 49 个覆盖缺口，不属于本文的执行失败。

回执 SHA256：`c0b4775d6fca2a1194c6ce5e8b384e701cf0c4a8e898eafa1f0eaf318e13fd62`。
主机 results 的规范 JSON SHA256 与回执一致：`6e681e9d68dc4c48ffeb4b13bcd7fc4b4c49bd3b2b8c85c01938fc4fb27581a1`。通过重建源 QA 行（包括排名 rn）并比较 `sample_sha256`，全部失败均匹配到历史请求；只读取元数据和在主机上提取的响应结构，未导出正文、凭证或用户身份。

## 逐项结论

序号为原 results 数组的零基下标；“历史耗时”属于原请求，不能当成本次 replay 耗时。HTTP 0 表示客户端没有取得响应状态。

| 序号 | 模型 / 请求 | 回放结果 | 历史耗时 | 证据与归因 |
|---|---|---|---:|---|
| 1 | claude-opus-5 / Chat 非流式 | HTTP 0 | 50.055 s | 历史成功耗时超过客户端 30 s 等待；回放预算不兼容，超时为首要嫌疑，具体异常未留存。 |
| 2 | claude-opus-5 / 记录为 Messages 非流式、有 tools | HTTP 0 | 0.377 s | 历史响应只有 `input_tokens`，请求无 `max_tokens`，符合 count_tokens；采集代码确实把 count_tokens 归一化成 Messages 后写入 blob path。该旧样本动作存在歧义，回放却向 `/v1/messages` 发请求。不能猜回正确路径，也不能把它视为生成接口回归。 |
| 4 | claude-sonnet-4-6 / Messages 流式 | HTTP 200，未通过 | 309.535 s | 历史成功耗时超过 120 s 读取上限；捕获响应达到容量上限，不能靠其前缀判断完整终止。本次失败可能是回放超时，也可能是流错误，旧记录无法区分。 |
| 11 | claude-opus-4-8 / Chat 非流式 | HTTP 0 | 340.322 s | 历史成功耗时超过 30 s 等待；预算不兼容，本次具体异常未留存。 |
| 21 | glm-5.3 / Chat 非流式 | HTTP 0 | 72.275 s | 同上。 |
| 25 | gpt-5.6-sol / Chat 流式 | HTTP 200，未通过 | 186.744 s | 历史首 token 1.885 s，但完成耗时超过 120 s；响应 capture 截断，本次具体流失败原因未留存。 |
| 26 | kimi-k3 / Chat 非流式 | HTTP 0 | 163.283 s | 历史成功耗时超过 30 s 等待；预算不兼容，本次具体异常未留存。 |
| 38 | claude-opus-4-6 / Messages 非流式 | HTTP 0 | 119.459 s | 历史成功耗时超过 30 s 等待；预算不兼容，本次具体异常未留存。 |
| 39 | claude-opus-4-6 / Messages 流式 | HTTP 200，未通过 | 59.809 s | 历史 SSE 已含 `type=error` / `upstream_error`，无 message_stop；同源 ops 记录为 502、owner=provider、phase=upstream、source=upstream_http。QA success 只按 HTTP 状态派生，回放把历史失败当成功基线。本次是否同因不可确认。 |
| 42 | claude-opus-5 / Messages 非流式、无 tools | 未发送，key_not_replayable | 3.345 s | 原 key 为 quota_exhausted，更新时间为回放前一天 17:41:19 UTC。此用户该模型历史候选均来自同一 key，没有有效 key 可替代；这是配额状态，不是网关代码回归。 |
| 43 | claude-opus-5 / 记录为 Messages 非流式、有 tools | 未发送，key_not_replayable | 0.390 s | 同一配额耗尽 key；还具有序号 2 的 count_tokens 路径歧义，但实际阻断发生在发请求前。 |

## 修复范围

- **回放预算**：移除固定 30 s socket / 120 s body 上限，使用 `max(600 s, 历史耗时 × 2 + 30 s)`、单请求配置上限 1800 s；读取按剩余预算设置 socket timeout，整体 5400 s alarm 继续约束批次。预算更长可能使批次触发整体截止，仍判 red。
- **可诊断性**：记录固定词汇的 reason、phase、耗时、收到的字节数、响应头耗时、源 request ID、源 key ID、源耗时与 client/response request ID。区分无响应、超时、传输错误、截断、SSE 错误/缺终止、编码错误和字节上限；不保存响应正文或异常原文。
- **动作保真**：QA middleware 在 handler 改写路径前保存不含 query 的原路径，blob 新增 `request.original_path`；归一化 `path` 与 DB endpoint 保持既有用途。回放优先读取原路径，对旧 count_tokens 歧义报 gap，禁止从响应猜路径。
- **样本资格**：回放不能把 `qa_records.success=true` 当成语义成功证明；明确含 JSON/SSE 错误的历史样本须尝试下一候选，否则记 gap。截断响应前缀缺终止本身不证明原请求失败。无效 key 同样尝试原样保留的下一候选；绝不把另一个 key 装到同一请求上。执行仍复核隔离快照中的 key。
- **SSE 解析**：按事件合并多行 data，识别 event:error；保留终止后错误仍失败的规则。

没有证据支持修改生产网关的路由、计费或供应商适配代码。当前确认的是 QA 路径保真缺陷及回放器预算、样本资格和诊断缺陷；两条 key 阻断是配置事实，历史 502 是供应方失败事实。

## 证据边界与验收

本轮隔离 app 使用 `log-driver=none`、`OPS_ENABLED=false`，隔离数据库与响应正文已按生命周期清理。us4/us5/us6 在回放窗口的保留日志未匹配到本轮 client marker 或三个 response request ID。因此，不能把历史耗时或历史 502 冒充本次失败的唯一根因，也不能宣称修复后 11 条必然全绿。需要今后经授权重跑，使用新增诊断字段验证；本 PR 不重跑付费生产请求。

行为回归覆盖真实 loopback 延迟响应、超时阶段、截断/字节限额、SSE 错误/终止与多行事件、动作路径、历史错误过滤、原 key 候选替换；QA 测试覆盖路径入 blob 及 handler 改写前采集。原 red 回执、覆盖分母、人工审核和切流门禁不变。
