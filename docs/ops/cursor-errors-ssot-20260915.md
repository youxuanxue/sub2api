# Cursor 错误核查与 SSOT 修复 — 2026-09-15

承接 [Messages 验证记录](cursor-messages-validation-20260914.md)。检查生产 #150 的 2026-09-13 00:00 UTC 起留存日志，并从生产出口对两个 Fable 模型各发送一次合成文本请求。账号保持暂停，分组 `[1,19]`，无 GPT mapping；没有接受数据政策或修改账号配置。

## 错误逐项结论

| 错误 | 证据与根因 | 处理状态 |
| --- | --- | --- |
| Fable 5 / Fable 5.1 旧通用 502 | 两次新探测均为 Connect `failed_precondition`、supplier 58、`MODEL_BLOCKED`、`retryable=false`、`action_required=config`。原文要求确认 Claude Fable 5 的数据保留政策 | 账号侧前置条件未完成，需要账号所有者阅读并决定是否接受。此类配置要求不归入 cyber/usage policy 会话隔离 |
| Opus 5 `cyber_policy_review` | 19:41 Asia/Shanghai 的已留存续接拒绝为 supplier 13 / `CONTENT_POLICY`，并非工具参数错误 | 上游拒绝仍存在；保持通用 cyber policy 处理，未重发该被拒请求 |
| HTTP/1.x malformed HTTP response | 留存日志有 9 个最终失败、1 个恢复成功，响应字节属于 HTTP/2 帧 | 当前已有 Cursor 专用 HTTP/2 profile、禁止 HTTP/1 降级和回归测试。暂停后无新流量，不能用“无错误”声称生产链路重新验收 |
| ResumeAction 空根槽、supplier 57 / provider 400 | 前一记录已有单变量复现、修复及 18 格成功证据 | 保留已上线修复；不能把后来的 supplier 13 当成旧错误回归，也不能把包装层 429 当成已证实限流 |
| HTTP 层 Connect policy JSON 丢失分类 | 流内 trailer 解析 details，HTTP 非 200 原先只记录正文；新回归复现 policy mark 缺失 | 本次修复：HTTP / trailer 共用错误结构和解析，接入现有 policy bridge；结构化 message-only usage 拒绝同样接入 |
| 原生传输取消/网络错误类型丢失 | RunAgent 原先新建普通错误，Messages 又可能将其变成 HTTP 502 | 本次修复：安全包装保留 cause，Messages 返回原始传输错误，让共享 transport owner 判断；取消不生成 failover，超时保留超时类型 |
| 原生错误无法按网关请求/账号关联 | 16 条旧 `cursor_messages_run_agent_failed` 均缺网关 request_id 和 account_id；新代码虽有 native ID，仍用全局 slog | 本次修复：使用 request-context logger，关联 gateway/client request、account、model、native request 和 Messages ID |

数据库共 26 条相关错误记录，其中 11 条为 recovered upstream event，15 条为最终失败；不能将 26 条全部算成用户失败。旧 Fable 错误缺原生 details，新探测证明当前前置条件，但不能反推所有历史 Fable 样本的唯一原因。

## Policy SSOT

- `gateway_tk_cursor.go::cursorPublicPolicyError` 只把原生结构化事实翻译到通用词汇：`cyber_policy_review → cyber_policy`；usage wording 交给通用 detector。
- `openai_usage_policy_tk.go::markOpenAISafetyPolicyEvent` 统一分类；`openai_native_messages_policy_tk.go` 仅渲染 Messages / Chat / Responses 对应错误。
- `openai_gateway_handler_tk_cyber_policy.go::recordCyberPolicyIfMarked` 和既有 session-block owner 负责会话隔离、一次性记录；不新增 Cursor 独立策略。
- 两种 policy 都停止本请求的重试/账号 failover，不因拒绝冷却账号。保留通用体系已有区别：cyber 有专门风控记录与免费错误用量审计，usage 有对应 ops 记录；本次不将两类风控和计费语义强行合并。
- 原生 HTTP 429/503 即使通常可重试，命中 policy 后仍只有一次上游尝试、一次终态错误，无成功结算结果。
- HTML、无错误结构的 JSON、任意 diagnostic map 和回显内容不作为 policy 证据；未解码内容仅保留脱敏诊断。

## 新的 Fable 证据

生产出口、当前主线加本次诊断修复的 service integration；HTTP ingress 为进程内 recorder，不是已部署网关路由、落库计费或 UI e2e。日志中的 account_id=42 是测试 fixture ID；凭据取自生产 #150，未写回账号。

| 模型 | Native request ID | 结果 |
| --- | --- | --- |
| `claude-fable-5` | `1f76d1a6-b8b7-4689-b9e8-74f301376ee4` | 数据保留政策未确认，首请求终止，2.28 秒 |
| `claude-fable-5-1` | `bfbedcea-9afd-4dbf-934b-f162cecd3c0e` | 相同前置条件，首请求终止，2.41 秒 |

上游没有给出本次政策的完整条款；不能从 `action_required=config` 推断条款内容，也不能代替账号所有者接受。下一步在 Cursor 官方模型入口查看并决定，完成后再逐模型单次复验。

## 验证与边界

- HTTP policy 丢分类和传输错误丢 cause 均有修复前失败、修复后通过的回归。
- 原生传输矩阵覆盖 3 协议 × 2 输出模式 × 14 结果，共 84 格；核对 policy mark、单次尝试、无成功 result、单个错误、日志关联和取消无 failover。
- 增加非结构化 HTTP body 负向测试，以及 URL 含凭据时的 cause 保留/公开错误脱敏测试。
- 最终全量后端测试、lint 和 preflight 以 PR 验证记录为准。本次未部署，生产调度与计费验收仍未关闭。
- 两个探测临时目录、远端凭据快照、签名 URL 和 S3 二进制传输对象已清理。
