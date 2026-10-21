# Kiro 文件工具分块运维说明

适用于 [已批准的分块方案](approved/kiro-file-tool-chunking.md)。部署含此修复的版本后，可为指定 Kiro 账号显式开启；默认关闭。该策略通过提示缓解大工具参数长静默，不是上游可用性保证。

## 开启与关闭

使用既有、需要管理员认证的 API；先用 `GET /api/v1/admin/accounts/{id}` 确认目标账号 `platform=kiro`，记录 `.extra.kiro_file_tool_chunking` 原值。不要把 token、完整 credentials 或账号响应写进报告。

通过 `POST /api/v1/admin/accounts/bulk-update`，即使单个账号也使用显式 `account_ids`，请求体如下（将 ID 替换为已确认的目标）：

```json
{"account_ids":[123],"extra":{"kiro_file_tool_chunking":true}}
```

批量接口按 JSONB 顶层键合并，保留其他 Extra。普通账号 PUT 的 Extra 有替换语义，不要只提交这一键去覆盖现有对象。不要同时提交 priority、credentials、group_ids、proxy_id 或 filters。

检查接口返回的成功/失败 ID，再 GET 目标账号确认开关为布尔 `true`。账号更新沿用仓库现有调度快照刷新路径；后续实际 Forward 读取选中账号的该字段。建议在新会话边界启用，因为提示变化会改变 conversation/cache 前缀。

回退使用同一接口发送布尔 `false`，再读取确认。字符串 `"true"`、数字、null 或缺失均不会启用。已有进行中的请求继续使用其已构造的 payload。

## 验证效果

只有声明 Write、Edit、MultiEdit 的请求受到提示影响；其他工具名不作推断。由客户端执行工具、回传真实结果、发起下一轮。遇到截断或已输出后断流，网关仍报错，不自动重放工具任务。

对少量已授权账号验证完整文件任务：检查实际选中账号、工具调用/结果配对、原文件保留、最终文件校验值、终止原因、物理请求次数、上游静默时长和 credits。对比时控制任务、输出长度、模型及代理，不能用失败且缺 metering 的请求记作零成本。

上次实测的 600 行任务经 21 次模型请求完成，消费约 15.77 credits；这不是固定报价或 SLA。MultiEdit 的真实长任务尚未独立验收。失败时保留 QA raw 完整度和请求标识，避免把“分块开启”误认为已排除上游故障。

本次交付只包含代码、测试和文档，未修改生产账号或 Tokensea 优先级。
