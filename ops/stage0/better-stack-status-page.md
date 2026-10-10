# Better Stack Status Page（Free）— **已上线**

TokenKey 公开状态页：`https://status.tokenkey.dev`  
托管：Better Stack Free（CNAME → `statuspage.betteruptime.com`）  
自建 Caddy status vhost：**已移除**（不再支持 `STATUS_SITE_DOMAIN`）。

## 验收（2026-09-07）

- DNS：`status.tokenkey.dev` CNAME → `statuspage.betteruptime.com`
- `https://status.tokenkey.dev/` → Better Stack「TokenKey status」
- Monitor：`https://api.tokenkey.dev/health`

## Staging 旁路 monitor（可提前，不影响正式）

在 Better Stack Uptime 另建一条（**不要**替换正式 monitor）：

| 字段 | 值 |
|---|---|
| URL | `https://api-hz.tokenkey.dev/health` |
| 期望 | HTTP 200 |
| 用途 | Hetzner prod staging 旁路；Wave B 前即可 |

公开状态页继续只展示正式 `api.tokenkey.dev`。

## Wave B 后：静音旧 EC2 CloudWatch（清单；切前勿执行）

| Alarm | 切后动作 |
|---|---|
| `tokenkey-prod-cpu-sustained-high` | `aws cloudwatch disable-alarm-actions --region us-east-1 --alarm-names …` |
| `tokenkey-prod-data-volume-used` | 同左 |
| `tokenkey-prod-root-volume-used` | 同左 |

完整步骤见 `deploy/hetzner/WAVE-B-PROD-CUTOVER-RUNBOOK.md` §B8。

## 运维备忘

- 勿再把 `status` 指回 prod EC2 A 记录。
- 勿再在 Caddy 恢复自建 status vhost（会与 Better Stack DNS 冲突）。
- 成本：Free；不要开 Telemetry / 额外 Status 付费项。

官方自定义域：<https://betterstack.com/docs/uptime/custom-subdomain/>
