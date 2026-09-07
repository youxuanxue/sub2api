# Better Stack Status Page（Free）— **已上线**

TokenKey 公开状态页：`https://status.tokenkey.dev`  
托管：Better Stack Free（CNAME → `statuspage.betteruptime.com`）  
自建 Caddy status vhost：**默认关闭**（`STATUS_SITE_DOMAIN` 空）。

## 验收（2026-09-07）

- DNS：`status.tokenkey.dev` CNAME → `statuspage.betteruptime.com`
- `https://status.tokenkey.dev/` → Better Stack「TokenKey status」
- Monitor：`https://api.tokenkey.dev/health`

## 运维备忘

- 勿再把 `status` 指回 prod EC2 A 记录。
- 若临时要自建 Caddy status：`STATUS_SITE_DOMAIN=status.tokenkey.dev ops/stage0/sync_caddyfile_via_ssm.sh prod <i-…>`（会与 Better Stack DNS 冲突，一般不要）。
- 成本：Free；不要开 Telemetry / 额外 Status 付费项。

官方自定义域：<https://betterstack.com/docs/uptime/custom-subdomain/>
