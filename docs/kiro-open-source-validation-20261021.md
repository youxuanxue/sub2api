# Kiro 开源修复逐项核实与 #46 实测

基线：[TokenKey #2764](https://github.com/youxuanxue/sub2api/pull/2764)，`3ac329ec4c66c1829d1363ed0de871233b63d3b5`。实测日期 2026-10-21 UTC。本报告区分真实上游调用、真实网关函数的合成故障注入、外部源码测试；三者均不等同于客户原项目完整重放或生产全链路验收。

## 后续修复状态（本分支，尚未部署）

用户确认后已按 [流、规则与刷新审批基线](approved/kiro-stream-auth-reliability.md) 修复下面表格中的前四项以及工具 ID 迟到；表格仍保留修复前证据，不代表当前分支仍有这些已覆盖反例。

- EventStream 复用 AWS SDK CRC/header 解码，前置长度与 prelude CRC 校验；非法 JSON/原始 UTF-8/孤立 UTF-16 转义、终止后的内容或重复终止明确失败。干净 EOF 与截断帧区分；协议损坏不再被当作连接故障跨 endpoint 重发。合法重复文本和终止后的 metering/context usage 保留。
- 顶层 system 只替换精确 Claude Code 身份行，其余规则和未知模板保留；取消关键词命中后的整段删除。
- 唯一匿名工具前缀在真实 ID 到达后绑定，完整合法参数完成后交付；缺失 ID、歧义前缀、重复完成 ID 拒绝。
- OAuth 刷新接受取消上下文，保留省略的 refresh token，拒绝空 access/无效或溢出有效期/显式空 refresh；profile 查询取消旁路刷新。原有刷新锁与保存 owner 不变。
- OIDC region 严格限制格式并复核目的主机；认证请求禁止重定向，响应大小受限，错误不回显凭据响应。

修复前反例 `/tmp/tk-kiro-reliability-red.log`；聚焦回归 `/tmp/tk-kiro-reliability-focused.log`；Kiro + OAuthRefreshAPI 回归 `/tmp/tk-kiro-reliability-regression.log`。US-071 将正负向断言登记为常驻测试。原来部分测试帧的 CRC 为零，本次更新为真实 CRC，避免旧 fixture 冒充合法协议。

真实验证使用 Claude Code 2.1.305 从隔离目录生成的 system（约 2.8 万字符，含追加规则），通过修改后 Forward、account 323 的代理及 canonical TLS 调用 Opus 4.6、5.5。两模型各三轮 Read → Edit → end_turn；客户端实际读写合成 corpus.txt 并回传真实结果。最终文件只将 OLD 改为 NEW，原有中文/emoji 与结尾保留；末轮正确报告追加规则标记 KI_RULES_7291。六次请求均单次物理调用，独立 raw CRC/文本/工具参数核对一致，总计 0.642056 credits、模型调用约 20.4 秒。证据摘要 `/tmp/tk-kiro-reliability/live.json`。这不是实际 Claude Code 客户端经生产入口的完整 e2e，也没有实测强制轮换生产 refresh token；不声称所有提示拒答率为零、全部历史事故已消除或 EU 账号已支持。

## 已发现缺陷：影响与开源方案是否有效

| 缺陷 | 对 TokenKey 网关的实际影响 | 已运行验证与开源方案结论 |
| --- | --- | --- |
| EventStream 缺少 CRC、严格 JSON/UTF-8 和终止后状态校验 | 在真实 Forward 的流式与非流式路径注入坏 CRC、坏 JSON、非法 UTF-8、终止后文字，均仍返回成功；可静默缺字、产生 `�` 或追加内容。返回成功结果不等于本测试实际写入了计费 DB | Kiro-Go 原测试通过；追加相同反例后 CRC、坏 JSON 被拒绝，但非法 UTF-8 与终止后文字仍漏过。不能整套移植视为修复完成。kiro2cc parser 原测试通过，但其 streaming handler 对解码/事件解析错误只记 warn 后继续（源码确认），不能因底层校验 CRC 就认定网关会失败关闭；未为它补齐同一整套服务对抗矩阵。TK 应独立补全严格校验、帧分配上限和服务层失败断言 |
| 顶层 Claude Code system 整段替换 | 客户追加的“已完成文件不要重做”等规则在转换时丢失，可能破坏项目约束；不能据此认定原客户重做必由它导致 | TK 反例复现；kiro2cc 实际 history/cache 测试通过，包含 system 内容改变、动态规则经 Read/Edit 后保留、辅助请求隔离。保留规则的转换方案有效；未在真实 Kiro 上验证完整 CC system 保留后的拒答率，因此不能直接关闭现有过滤 |
| OAuth 刷新响应与旁路 | 实际 KiroTokenRefresher 接受空 access、负 expires，并以空 refresh 覆盖旧值；取消后实际 HTTP 请求没有取消上下文。profile 查询旁路返回新 token 却丢弃，轮换时可能使账号后续刷新失败 | TK 刷新服务 mock transport 复现；既有 OAuthRefreshAPI 锁、重读、保存、取消后不落库测试通过，应该复用。AIClient2API 原方法提取运行，合并 12 个并发刷新、保留省略的 refresh、保存轮换 token、拒绝空 access 均通过；负 expires 仍接受。它也不是完整可直接照搬的方案。未人为轮换生产凭据 |
| 未校验 OAuth region 可改变目的主机 | 管理/导入凭据中恶意 region 会把携带合成凭据的请求构造到其他 host；是受管理入口影响的凭据外发风险，非已证明匿名公网利用 | TK mock transport 复现；CLIProxyAPI 插件原 region 工具与原测试提取运行，合法区域、authority 破坏输入、最终 HTTPS/host、非法非空区域拒绝均通过。该校验有效；真实凭据未发往异常站点，重定向完整链路仍需补测 |
| auth region 与 profile 数据区域混用 | EU profile 的实际生成请求仍指向 US runtime，存在区域不匹配/403/模型不可用风险 | TK 构造请求复现；Kiro-Go 原区域解析与 regionalize 测试通过。应以合法 profile ARN 裁决数据区域，OIDC region 独立。没有 EU 生产账号实测，不能保证修 host 就能解决全部授权错误 |
| 工具 ID 迟到 | 唯一工具首片有 name/input、后片才有 ID 时，Forward 流式与非流式都失败，返回可 failover 的 502，未交付工具调用 | TK 服务层反例复现；同一合法序列在 Kiro-Go 成功且真实 ID 保留。该状态管理有效，但必须继续拒绝歧义匿名并发、重复完成 ID、坏 JSON；绑定真实 ID 前不能对外输出可执行工具 |
| 巨大 Write/Edit 参数长静默（#46） | 本次真实单次大 Write 收到少量内容后长静默并 unexpected EOF，客户任务中断；当前网关错误终止且没有已输出重试 | 原版 300 行提示首轮成功，但实际仅生成 100 行；字节/行数双限制多轮实测及按账号原型见下一节。提示不能保证消灭上游断流 |

源码固定版本：Kiro-Go `f8f6071c9298a4266ad3e0c7e483d4a2510cbcaf`；kiro2cc-proxy `afa36813fb1d1e3cc890376227368ffe2b01df19`；AIClient2API `acdfa2c5741fbc05f19cc65297b816e8cdab6fd2`；CLIProxyAPI Kiro 插件 `f7b60a605c3613c18e4011b922f93b5bad70522c`。

相关实现：[Kiro-Go parser](https://github.com/Quorinex/Kiro-Go/blob/f8f6071c9298a4266ad3e0c7e483d4a2510cbcaf/proxy/kiro.go)、[kiro2cc history](https://github.com/TsinHzl/kiro2cc-proxy/blob/afa36813fb1d1e3cc890376227368ffe2b01df19/src/anthropic/converter/history.rs)、[AIClient2API refresh](https://github.com/justlovemaki/AIClient2API/blob/acdfa2c5741fbc05f19cc65297b816e8cdab6fd2/src/providers/claude/claude-kiro.js)、[region 校验](https://github.com/xiaokui-dev/cliproxyapi-kiro-plugin/blob/f7b60a605c3613c18e4011b922f93b5bad70522c/internal/kiro/region.go)。

外部 Rust 测试首次因源码包缺 UI dist 无法编译；仅补空静态资源目录后运行后端测试，没有声称构建或验证其 UI。Go 外部模块采用 external link 解决本机 dyld LC_UUID 问题。AIClient2API 和 region 为原方法/工具提取测试，依赖受控替身，不冒充整仓集成测试。

## #46 真实上游对照

通过受控探针使用 account 323、绑定 proxy 4、canonical Kiro CLI TLS profile、真实 Kiro Opus 4.6。底层使用本仓 Forward/translator/repository transport；未经过生产认证、调度、计费完整链路。请求只生成合成自然景观文本，无客户源码。

| 样本 | 结果 | 总耗时 | 最长响应体读取间隔 | 上游 credits |
| --- | --- | --- | --- | --- |
| 无分块提示，单次 20 行 Write | 成功，完整 TOOL_USE | 35.469s | 32.084s | 0.441073 |
| 无分块提示，强制单次 600 行 Write | 失败：unexpected EOF；没有 terminal / message_stop；一个物理请求 | 393.139s | 389.976s | 未收到 metering；费用未知，不能记为 0 |
| 开源原版 300 行提示，600 行总任务首轮 | 成功，但本轮实际只有 100 行、约 13.4KB 文本参数，尚非完整任务 | 131.116s | 127.817s | 1.550932 |

原版提示中“约 4 分钟一定中止”是开源作者的经验，不是本次可验证的 Kiro SLA。诊断首输出预算 360 秒、请求预算 600 秒；本轮没有改生产 timeout。Body.Read 分片可在生成完成后密集到达，看到数百 toolUseEvent 不代表生成期间持续传输。下游 SSE ping 无法使上游提前发出工具参数。

新的合成 600 行任务使用每调用 6000 UTF-8 bytes / 150 行指导，通过客户端真正执行 Write/Edit，将真实工具结果放进下一轮历史；对每次 Edit 验证唯一 old_string 命中和先前完整文件前缀保留。客户端采用有界轮数、时间与 credits，停止后可从完整历史和真实工具结果恢复。保护 fixture 不代表模型已遵守客户项目全部约束；工具 runner 限定只能改 corpus.txt。

完整结果：21 次模型请求、20 次真实工具执行（1 Write + 19 Edit），最终正常 `end_turn`，共 600 行、100593 UTF-8 bytes；编号严格 1–600，无重复整句、替换字符或检测到的省略占位。每次追加保留完整旧文件前缀，全部工具执行成功。最初客户端在第 16 轮达到预设轮数上限，已完成 480 行；恢复后从第 17 轮继续，未重写前文。第 17–21 轮使用网关原型，从临时账号副本开关注入同一提示，而非客户端直接携带提示。

累计模型调用时间 978.548 秒（约 16.3 分钟，不含两段之间的操作等待），credits 15.765089。最长 Body.Read 间隔 53.215 秒；最大合计文本参数 6270 字节，3 次超过建议的 6000 字节。**这证明提示有缓解效果，也证明它不是硬限制。** 原版 300 行提示只跑首轮；失败的单次基线未返回 metering，且各样本生成长度不同，因此不能宣称成本增加了某个精确倍数。

完整 raw audit 逐轮核对工具 ID、解析后的参数、可见文本；工具名按既有映射从上游 `write`/`edit` 恢复客户端声明的 `Write`/`Edit`，并非参数改写。最终文件 SHA256 `09b9284723c971692a592d9d7c67af5e04c25330a165b0425f4f0fde804e0a03`。

MultiEdit、大图、原客户截断历史、不同模型/账号和重复大样本尚未在此试验覆盖，不以本次结果宣称全部修好。此任务平均行长高于要求的“约 40 个汉字”，模型输出尺寸也不是严格遵循；不能仅凭行数达标就宣称全部自然语言要求精确满足。

## 其余研究项的处理边界

- 合法重复 delta、正常工具历史、工具结果图片转换、取消、缺终止事件和已输出后禁止重试：属于已有能力。上轮真实 Claude Code 工具循环证据仍有效；本轮原型未改这些 owner，相关 KiroGateway 回归通过。没有重新跑真实图片识别旅程。
- ANSI 显示码：未找到 TokenKey 引入该码的同类路径，不把 kiro2cc 的 UI 问题归成 TK 新缺陷。
- payload 自动裁剪、context usage 观测、重复 metering 计费语义：属于尚需采证的风险。本次不改计费、不把重复事件盲目去重，也不声称已证明上游 snapshot/delta 语义。
- `tool_choice`、defer_loading/tool_reference、原生 web_search/document：前轮列为待验证兼容面，不是已证实客户事故根因；本轮尚未做完整能力矩阵。
- kiro-lb 的刷新租约、jwadow 的合成恢复等只作架构参考，本轮没有运行它们的整仓服务，不把阅读源码标成实测通过。已通过测试的刷新锁优先复用 TK 原 owner。

## 落地范围与证据

[已批准的按账号分块方案](approved/kiro-file-tool-chunking.md)。提示会增加调用轮数、上下文重复输入、任务耗时及费用，不能默认为无成本修复。本轮调研时原型尚未应用到产品源文件。用户随后确认了取舍，本分支已将相同提示与注入行为落入正式代码，并补常驻测试；尚未合并、部署或修改生产账号。Tokensea priority=1 未修改。

本地证据 `/tmp/tk-kiro-validation/`：

- `gateway-impact.log`、`late-identity-gateway.log`、`refresh-service.log`：当前 TK 缺陷反例，预期正确性的断言为红，不能称修复后通过。
- `refresh-owner.log`：现有刷新协调 owner 测试通过。
- `upstream-kiro-go.log`、`upstream-adversarial.log`、`upstream-history2.log`、`upstream-tools.log`、`upstream-rust-parser.log`、`upstream-auth.log`、`upstream-region.log`：各开源方案运行输出。对抗用例失败明确保留。
- `small-write.json`、`large-write.json`、`upstream300.json`、`byteguard.json`、`byteguard-resume.json`、`loop-audit-final.json`：直连实测、多轮恢复与 raw 对照摘要。
- `prototype-tests.log`、`prototype-forward-tests.log`、`prototype-integration-all.log`、`prototype/chunking.patch`：可复测原型、完整 Kiro integration 包与服务回归。

原事故 raw 截断，不能完整复原客户原请求，更不能以捕获前缀 hash 证明完整重试循环。本次非法 UTF-8 是合成注入；原事故响应未发现 U+FFFD，二者不能混为事故根因。
