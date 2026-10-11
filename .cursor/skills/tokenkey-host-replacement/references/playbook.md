# Host replacement — 命令骨架

细则：`deploy/hetzner/README.md`、`RUNBOOK-disaster-recovery.md`。纪律见上级 `SKILL.md`。

## Edge → Hetzner

**Provision：** `deploy/hetzner` render → cloud-init → SSM Hybrid → dispatch。

**Replicate：** 源 `tokenkey-pgdump.sh`（precious）+ logs data-only → S3；目标 restore；对账 `accounts` / `usage_billing_dedup` / `usage_logs`。

**Cutover：** Porkbun A→NEW_IP；单域名 `API_DOMAIN`；CIDR 取 Hetzner README；sync caddy；stub `https://`；prod pin+restart。

**Drain：** 旧机停 app（+ gemini-web）；留 PG/Caddy/Redis ≥7d。

**Verify：** `dig +short <host> @8.8.8.8`；`/health`；smoke；disk-metrics/Feishu/ghcr-prune。

## Prod / LS recreate

- 无卷/跨云：DR §4.4；卷在：优先 §3。  
- LS recreate：expansion + 显式 `recreate=true`。

**回滚：** 正式 A（及 prod pin）改回旧 IP；勿双写。
