# Gemini 原生模型接入证据

用户要求优先由 Vertex / Antigravity 承接 universal key 的
`gemini-3-flash-preview`、`gemini-3.1-flash-lite`。

## 真实上游探测

时间：2026-09-08 02:29:48–02:31:28 UTC（北京时间 10:29:48–10:31:28）。
通过 SSM 在账号所在主机内读取凭证并发出小额生成请求；凭证没有回传。
Vertex 使用 `aiplatform.googleapis.com` 的 `global` Gemini SSE 端点；
Antigravity 使用 us4 原生 OAuth 的 `daily-cloudcode-pa.googleapis.com` SSE 端点，
请求 envelope、身份提示与 CLI 1.1.27 UA 对齐当前网关实现。
输入为 `Reply OK only.`，`maxOutputTokens=1024`。

| 路径 / 账号 | 请求上游模型 | HTTP | 可见输出 | 输入 / 输出 token | 思考 token |
| --- | --- | --- | --- | --- | --- |
| Vertex prod 47 | gemini-3-flash-preview | 200 | OK | 4 / 1 | 43 |
| Vertex prod 57 | gemini-3-flash-preview | 200 | OK | 4 / 1 | 60 |
| Vertex prod 58 | gemini-3-flash-preview | 200 | OK | 4 / 1 | 56 |
| Vertex prod 59 | gemini-3-flash-preview | 200 | OK | 4 / 1 | 61 |
| Vertex prod 74 | gemini-3-flash-preview | 200 | OK | 4 / 1 | 46 |
| Vertex prod 47、57、58、59、74 | gemini-3.1-flash-lite | 均 200 | 均 OK | 均 4 / 1 | 均 0 |
| Antigravity us4 原生 5（prod 镜像 62） | gemini-3.1-flash-lite | 200 | OK | 258 / 1 | 0 |
| Antigravity us4 原生 5 | gemini-3-flash | 200 | OK | 258 / 1 | 25 |
| Antigravity us4 原生 5 | gemini-3-flash-preview | 404 | NOT_FOUND | 无 | 无 |
| Vertex prod 57 | gemini-3.1-flash-lite-preview | 404 | NOT_FOUND | 无 | 无 |

成功响应均包含 `finishReason=STOP`，`modelVersion` 与请求上游模型一致。
这是上游生成与 token 证据，不是 TokenKey `usage_logs` 计费验收。
账号 58 虽有上游能力，生产调度仍停用；探测没有改变其调度状态。

## 修复与职责

- Vertex 共享映射及各能力 profile 纳入两个精确名称，并由现有区域 owner 选择 `global`。
- Antigravity 默认映射纳入 flash-lite；恢复 preview → flash 兼容映射，移除该别名的历史禁用条目。
- 更新既有 Gemini / Antigravity 目录 owner，由 Go 重新生成 model-surface bundle；原生 Gemini floor 按既有共用目录规则同步派生。prod 当前没有 `platform=gemini` 账号，此次实测覆盖 Vertex 和 Antigravity。
- 价格沿用现有 overlay；flash-preview 与其 Antigravity wire target 的输入、输出、缓存读取价格相同。
- 候选资格继续由 `protocolrouter.Plan` 与共享调度评估判断，沿用现有选组顺序；没有新增平台优先级分支或 converter。

回归测试：`TestCandidateEligibilityGeminiNativeModelAdmission`、
`TestCandidateEligibilityGeminiNativeEdgeMapping`、`TestCandidateEligibilityGeminiNativeModelPricing`。
前两项在修复前因模型准入失败；修复后覆盖原生 / 流式 / Chat 转换、Vertex → Antigravity 回退、空池容量错误及 edge 空映射默认值。

## 上线边界

此 PR 没有修改线上账号映射、runtime overlay 或部署镜像。
上线需先部署包含修复的 prod / Antigravity edge，再通过 `modelops activate`
以目标 bundle 和新的独立探测、价格证据显式激活 prod 映射；遵循现有 scope 证据校验，
不能把 Vertex 账号伪标成原生 Gemini 账号。Edge 空映射继续消费编译默认值。
之后复测两个原生入口与 Chat 入口，并核对实际账号、上游模型及非零计费记录。
