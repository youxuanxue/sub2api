# Upstream Issue 与中转实现跟踪调查（2026-10-21）

## 为什么最近没有对应 Issue

[2026-10-20 的 watchdog 运行](https://github.com/youxuanxue/sub2api/actions/runs/39517810206)
成功结束。下载 `issue-watchdog-upstream-1` 的 `report.json` 后，实测：
扫描时刻为 `2026-10-20T23:52:47Z`，代码为
`0f64bec7568a9f2ca23dc2e8a9e9b4f995920358`；上游 Issue 为 2,698 条，
`needs_review` 为 1,177 条，`high_unresolved` 为空。
这些数值是本次诊断的历史观测，不是需要持续维护的库存。

根因位于 [PR #2117](https://github.com/youxuanxue/sub2api/pull/2117)
（`fbb7288d8`，2026-09-11）后的行为组合：

1. 分类器把新的敏感关键词命中设为 `needs_review/candidate_unverified`，
   不再直接声称已确认高风险。这一证据边界应保留。
2. `sync_issues` 只允许 `high_unresolved` 新建独立 Issue。
3. 定时 `scan` 没有把待核实线索投递到持久队列，也不会自动更新人工判断账本。
   `fix` 只在显式手动 dispatch 时运行。
4. 线索只进入运行摘要和保留 7 天的工件，形成“采集成功，但无人可持续处理”的断点。

最近读取的连续运行均为成功，故不是整个定时任务停摆的证据。
只有 `upstream/anthropic` 两个来源；现有 `fetch_snapshot` 主动排除 PR，
初次采集仅包含 open Issue。这不能覆盖相关中转项目、近期已关闭的问题和已合并修复。
GitHub cron 还存在实际启动延迟，不能把 cron 时间当作及时处理的保证。

## 修复边界与跟进方式

相关网关调度、计费、账号和协议转换线索直接进入按平台划分的汇总 Issue，
不再以人工确认或 high_unresolved 为建单门槛。同平台来源共用一个 Issue（kiro、openai、google 各一个；主上游保留 upstream）；每日追加新增或更新
条目的评论，重复运行不重复投递，超长批次仅拆评论。已存在的独立 Issue 保留。

人工勾选或 TokenKey 修复账本中的有效证据会将条目从待处理集合剔除，即使上游还开着。
上游关闭或合并 PR 不代表 TokenKey 已解决。全部处理完才关闭汇总 Issue；出现新问题时
重新打开同一个 Issue。修复 PR 只关联汇总单，不能用 closing keyword 关闭整个汇总单。

中转项目目录由 [relays.json](../../ops/issue-watchdog/relays.json) 唯一拥有。
工作流、游标恢复、只读入口及采集边界见
[watchdog 契约](../../ops/issue-watchdog/README.md#candidate-review-and-relay-implementation-watch)。
此改动不自动合并、发布、改账号或调整模型目录。

## 调研取舍

Kiro 保留用户指定的实现，并同时跟进 `hank9999/kiro.rs` 和近期活跃的
`ZyphrZero/kiro.rs`；CLIProxyAPI 主仓和 Kiro 插件分开，因为插件的账号/区域处理
不由主仓发行记录完整覆盖。Kiro-Go 与 kiro-gateway 即使最近主分支提交较少，
仍有新的流处理问题和待合并 PR，不应仅按 stars 或 pushed_at 排除。

OpenAI 侧选择 CLIProxyAPI、AIClient2API、codex-lb、codex-proxy 和 codex-proxy-rs，分别覆盖
多协议转换、账号池/配额和 Responses 终态。检索到的 `lidge-jun/opencodex`
主要把其他供应商接入 Codex 客户端；`BenedictKing/ccx` 是更通用的 API proxy。
它们可作为后续协议研究候选，本轮优先覆盖订阅账号转 API 的直接供应实现。
这不是 OpenAI 官方项目背书，也不从 README 的能力声明推导 TokenKey 应开放的模型。

Google 侧区分 Antigravity OAuth、Gemini CLI 和 Gemini Web：
CLIProxyAPI/AIClient2API 提供跨协议参照；Antigravity-Manager 与
antigravity-claude-proxy 提供账号、配额、缓存和转换参照；Gemini-API 提供
Web 会话与流解析参照。gemini-cli-proxy 活跃度较低，保留观察但不作为唯一来源。
不把 Gemini Web cookie、CLI OAuth 和 Antigravity 的账号处理互相套用。

## 首批值得核对的已合并修复

以下合并状态已通过 GitHub PR API 实查。标题/PR 自述仅是外部证据，
不代表已在 TokenKey 复现；任何代码引入先检查源仓库许可证。

| 外部证据 | 问题与本地检查方向 | 当前判断 |
|---|---|---|
| [kiro2cc-proxy #53](https://github.com/TsinHzl/kiro2cc-proxy/pull/53)，2026-10-19 合并 | `tool_result.content[]` 图片被丢弃；核对 `integration/kiro/translator.go` 的 `extractToolResultContent`、当前/历史轮与孤立结果过滤 | TokenKey 已有图片提取实现；不能直接判为缺失，需核对过滤时图片归属场景 |
| [AIClient2API #717](https://github.com/justlovemaki/AIClient2API/pull/717)，2026-10-09 合并 | 按 `tool_use.id` 精确查函数名，避免猜测 ID 导致 Gemini `functionResponse` 400；检查 `pkg/antigravity/request_transformer.go` 与 `pkg/apicompat/gemini_messages_request_tk.go` | 需按 TokenKey 实际 converter 做配对复现 |
| [codex-proxy #877](https://github.com/icebear0828/codex-proxy/pull/877)，2026-10-21 合并 | `response.incomplete` 被误报 `end_turn`，丢失截断/拒答与 usage；检查 `pkg/apicompat/responses_to_anthropic.go` | 已有 incomplete 终态实现与测试；比较 content_filter、max_output_tokens、工具调用优先级 |
| [ZyphrZero/kiro.rs #123](https://github.com/ZyphrZero/kiro.rs/pull/123)，2026-10-18 合并 | HTTP 200 中 `CONTENT_FILTERED` 被当作空成功或上下文溢出；检查 `integration/kiro` eventstream 与 `service/kiro_gateway_service.go` | 已有 metadata stopReason 解析及 refusal 映射；还需核对后到 contextUsage 是否覆盖拒答 |
| [kiro-lb #115](https://github.com/minpeter/kiro-lb/pull/115)，2026-10-15 合并 | 每请求重建认证对象丢失刷新退避，撤销的 refresh token 持续 401→429；检查 TokenKey 共享凭证故障与 refresh owner | 该 PR 涉及其 InferX 特有生命周期，不直接复制；适合作为 TokenKey 反复刷新回归场景 |
| [AIClient2API #744](https://github.com/justlovemaki/AIClient2API/pull/744)，2026-10-20 合并 | Antigravity 免费层目录仍列出实际 404 模型 | 属供应层能力证据，交 modelops 核对；不可从对方名单直接改 TokenKey catalog/Plan |

## 按平台补充调研

- [codex-proxy-rs](https://github.com/zyycn/codex-proxy-rs) 的 README 明确定位为 Codex
  多账号 Responses 网关，纳入 openai。[#500](https://github.com/zyycn/codex-proxy-rs/issues/500)
  讨论 hosted web_search 的授权和计量；[#459](https://github.com/zyycn/codex-proxy-rs/pull/459)
  涉及客户端 installation ID，采集时仍未合并。
- [gemini-balance](https://github.com/snailyp/gemini-balance) 提供 Gemini API Key 轮询、
  配额和失败切换，纳入 google。[#405](https://github.com/snailyp/gemini-balance/issues/405)
  讨论 AI Studio 新 API Key 格式，[#328](https://github.com/snailyp/gemini-balance/issues/328)
  涉及代理路径的区域错误。README 声明 CC BY-NC 4.0，本任务只跟踪问题证据，不移植其代码。
- [openai-gemini](https://github.com/PublicAffairs/openai-gemini) 的供应端为 Gemini，
  OpenAI 只是入口协议，所以只纳入 google。[#107](https://github.com/PublicAffairs/openai-gemini/issues/107)
  涉及第二轮工具调用的 thought signature；该条目超过 30 天仍开放，首次回填需包含全部开放条目。

多平台项目按 Issue/PR 的标题、正文、标签和讨论中的平台词分流；无明确平台的通用问题
进入其清单声明的全部平台。此规则用于发现线索，不作为 TokenKey 受影响的证据。

## 当前讨论与尚未合并的线索

- [sub2api #8248](https://github.com/Wei-Shaw/sub2api/issues/8248)：Cloudflare 1010
  被计为账号 403 处罚；应核对供应商真实鉴权故障与出口拦截的分类。
- [sub2api #8234](https://github.com/Wei-Shaw/sub2api/issues/8234)：Grok 空 body 的
  402 被当作欠费而冷却号池；应核对账号池级故障传播边界。
- [kiro-gateway #287](https://github.com/jwadow/kiro-gateway/issues/287) 与
  [PR #288](https://github.com/jwadow/kiro-gateway/pull/288)：工具输入缓冲期间
  无输出，客户端断连；采集时 PR 尚未合并。
- [CLIProxyAPI #6838](https://github.com/router-for-me/CLIProxyAPI/pull/6838)：
  Claude 工具结果的错误语义经 Gemini 转换丢失；采集时 PR 尚未合并。
- [antigravity-claude-proxy #389](https://github.com/badrisnarayanan/antigravity-claude-proxy/issues/389)：
  Gemini 流式 usage 缓存 token 重复计数；需要 TokenKey 计费回归证据。
- [Antigravity-Manager #3720](https://github.com/lbjlaq/Antigravity-Manager/issues/3720)：
  持续限速/账号降优先级调查。讨论和个案不能直接当作账号策略或可用模型真相。

本次交付是监控断点修复和线索队列，不宣称上述行为问题已在 TokenKey 全部验证或修复。
本地只读扫描不会修改 GitHub Issue，也不会推进发布游标；新定时流程须合并到默认分支后才生效。

## 本次验证

- watchdog/relay Python 行为测试通过，覆盖无需审核即可建单、单 Issue / 评论拆分与重放、全部解决后关闭及重开、
  人工文本保留、评论独立更新、closed 与 merged 区分、旧扫描不覆盖新证据、
  同步失败游标保持和只读不确认投递。
- `go test ./internal/integration/kiro ./internal/pkg/apicompat ./internal/pkg/antigravity`
  通过；这是现有实现回归证据，不是所有外部案例已逐一复现。
- 目录中各来源均完成真实 GitHub 只读扫描；CLIProxyAPI 首次分页调用非零退出，
  重试成功，未将首次失败掩盖成成功。实时原始报告留在本地临时目录，未提交 Git。
- 用真实报告模拟初次投递与完整重放：评论大小未超限，重放无写操作。
