---
status: approved
approved_by: "feng (conversation: 对齐 origin/main，逐个核实并修复，开pr; implementation and PR only)"
approved_at: 2026-10-16
scope: verified OAuth consolidation gateway defects; merge, deployment and production account changes remain separate
---

# OAuth 集中到 prod 后的网关修复

用户授权核实并修复集中迁移审计发现的问题，提交可审阅的 PR。本基线授权代码与测试，
不代表应用已经发布，也不代表停用线上账号、变更供应或切流已经执行。
现网与云节点身份由 [舰队入口](../../deploy/hetzner/README.md) 拥有。

## 已核实的行为与修复

| 触发 | 原行为 | 修复后的契约 |
| --- | --- | --- |
| OAuth/setup-token 账号配置 ProxyID，但代理缺失、停用、过期或不匹配 | 多个路径可能使用空 proxy URL 直连 | 不可调度；发送前权威重读复核；刷新及后台隐私/订阅请求在网络前拒绝。查库失败不能判凭据失效。 |
| 空映射 Codex 账号遇到 `qwen3.8-flash` | 外厂模型前缀漏判，向 ChatGPT 上游发送后 400 | 共享模型能力判断识别带小数版本的 Qwen 家族；显式模型映射和无关自定义别名保留原语义。 |
| Kiro HTTP 400，JSON message 精确为 `Image format is not supported.` | 重试其他端点后按供应故障处理，最终 502 | 返回 typed invalid request 400；不重试端点/账号，不先输出成功流。不把同文案的 503 或 echo 字段误归类。 |
| SSE HTTP 200 已提交，后续出现最终 5xx | 发布巡检只数 HTTP 5xx，可能报告 green | 关联已完成请求的 ops 最终状态，逐 request_id 去重；恢复为 200 的尝试不算最终失败；缺证据为 unknown/yellow。 |

固定代理策略只作用于指定了 ProxyID 的 OAuth/setup-token；有意无 ProxyID 的直连和
API-key 既有策略保持不变。显式备用代理切换必须先持久化新的 ProxyID；本修复不新增
自动出口漂移。调度、权威执行和刷新复用同一代理就绪判断，owner 登记在
[候选资格 SSOT](candidate-eligibility-ssot.md#owners)。Kiro 图片判断由协议客户端与服务
错误分类共享，不能从回显的请求内容或任意 400 推断相同结论。

巡检使用有时限、只读的数据库查询；只关联日志窗口内已完成的 request_id。
数据库不可用、相关字段缺失时不能声称零失败。ops 异步写入仍有采样延迟，单次 green
不证明零业务失败；保留既有延迟巡检。最终 5xx 包括业务与运维请求，不用它单独计算用户 SLA。

## 逐项审计处置与剩余边界

证据窗口：2026-10-16 02:28:50–03:28:50 UTC（北京时间 10:28:50–11:28:50）；
运行版本 v1.8.297。容器访问日志仅覆盖其中 02:48:15 UTC 之后。
代码修复的单元/集成测试不能替代发布后的同窗验收。

| 审计项 | 结论 / 处置 |
| --- | --- |
| Kiro 图片失败 | 已确认真实上游 400，修错误分类和重试放大。图片原始格式尚无可用证据；不宣称已实现格式转换或所有图片已可用。 |
| Haiku 5 空池 | 当前启用的 Kiro 声明 Haiku 4.5，不声明 5；没有启用的原生 Claude OAuth 池。是供应缺口，不能伪造映射到 4.5。补供应或调整承诺模型菜单需要产品/运营决策。 |
| OpenAI 503 / protection unavailable | 上游过载/拒绝证据成立；已有语义输出时不可重放。现有 web-search 历史归一化已存在，未证明新网关根因；不清空历史或停用整个仍有成功流量的账号。 |
| 占位 Antigravity | 08:14 UTC 再次确认 #227 `uk1-ag-uk1-placeholder`、#243 `uk2-ag-uk2-placeholder` 仍启用、可调度，last_used 为空，24h 无用量；应停用并保留记录。尚未写线上。 |
| 重复 Antigravity 身份 | 再次确认 #273/#274/#275 同一非空 refresh token、同组/映射/代理，均无 parent、各 concurrency 90，24h 无用量。建议保留 #273（本次读回凭据到期最晚），停用 #274/#275；实施前再核对身份与凭据新鲜度，不能仅按 ID 删除。尚未写线上。 |
| 已报错 OAuth | 审计发现源副本亦有错误，冻结时间晚于复制；不能证明均为迁移引入。保持不可调度，逐个按供应商授权状态重新授权，不批量 clear-error。 |
| 冻结 edge 回滚 | 冻结副本不刷新，不能直接启用旧 token；恢复顺序见舰队入口。 |
| 代理名 / 物理节点 | 名称为历史逻辑池，不等于当前物理 edge；已验证出口可用且历史绑定一致，不交换 us4/us5 地址。维护按代理真实 endpoint 和依赖账号集合定位。 |
| HTTP2 诊断日志 | 缺请求/账号上下文，不能把诊断条数当用户失败或指认单个代理。 |
| 刷新与资源 | 观察窗 AG/Kiro 后台刷新成功、edge 冻结且无刷新；OpenAI/Grok 尚未到期，未验证后续实际轮换。未见 OOM、DB 阻塞或采样排队。 |

## 验证与回退

[US-064](../../.testing/user-stories/stories/US-064-oauth-consolidation-gateway-safety.md)
记录可执行测试与反例。核心验证包含：失效代理零网络调用、陈旧快照的执行前拒绝、
代理查库失败不隔离凭据、合法代理及显式直连保持、Kiro streaming/nonstreaming、
Qwen 映射边界、HTTP 与 in-band 错误去重、巡检失败返回 unknown。

应用回退使用已有发布机制回到上一镜像；这也恢复旧版的代理直连风险。
发生代理不可用时优先修复代理或显式更换绑定，不能以禁用 gate 作为常态恢复。
本 PR 不改变数据库 schema、账号凭据或现网启停状态，不包含新基础设施采购。
