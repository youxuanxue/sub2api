# TokenKey status page (legacy static)

旧实现：Caddy 静态页 + 当场 `GET /health`（无历史 uptime）。

**现行方案：Better Stack Free** — 见  
[`ops/stage0/better-stack-status-page.md`](../../../ops/stage0/better-stack-status-page.md)。

本目录仅在 DNS 切到 Better Stack **之前**仍可能被 `sync_caddyfile_via_ssm.sh` 同步；切流并确认公网走 Better Stack 后，可用 `STATUS_SITE_DOMAIN=` 关闭 Caddy status vhost。
