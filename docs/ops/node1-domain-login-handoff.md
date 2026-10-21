# Node1 域名交接范围

#2698 已改为 UCloud cn-prod `106.75.29.139` → node1，并保持现有公网入口和域名。`api.galaxywaveai.com` 新域名登录交接不属于本任务，不得作为迁移前置，也不得修改现有 AWS prod 或 UCloud cn-prod 的 OAuth、Turnstile、DNS、证书和客户端配置。

现有 cn-prod 域名、证书、登录回调和客户端 Base URL 在准备阶段保持不变。未来若业务需要更换域名，应另立高风险设计和切流审批，准备独立 OAuth/Turnstile 应用并完成回滚验证。

当前仅需验证现有入口在隔离 node1 环境的 TLS/SNI、Caddy、PROXY protocol、WireGuard、SSE、登录、API、usage 和计费行为；禁止向真实用户发送测试邮件或通知。
