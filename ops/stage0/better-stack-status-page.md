# Better Stack Status Page（Free）

公开状态页：`https://status.tokenkey.dev`  
托管：Better Stack Free（CNAME → `statuspage.betteruptime.com`）  
自建 Caddy status vhost：**已移除**（不再支持 `STATUS_SITE_DOMAIN`）。

## 现网

- DNS：`status.tokenkey.dev` CNAME → `statuspage.betteruptime.com`
- Monitor：`https://api.tokenkey.dev/health`
- 勿把 `status` 指回旧 EC2 A；勿在 Caddy 恢复自建 status vhost
- 成本：Free；不要开 Telemetry / 额外 Status 付费项

官方自定义域：<https://betterstack.com/docs/uptime/custom-subdomain/>
