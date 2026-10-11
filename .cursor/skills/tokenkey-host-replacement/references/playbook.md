# Host replacement — 命令骨架

细则：`deploy/hetzner/README.md`、`RUNBOOK-disaster-recovery.md`。纪律见上级 `SKILL.md`。

## Edge → Hetzner

**Provision：** `deploy/hetzner` render → cloud-init → SSM Hybrid → dispatch；稳态正式名（切流窗可短暂 `-hz`）。

**Replicate（冻写）：** 源 `tokenkey-pgdump.sh`（precious）+ logs data-only → S3；目标 restore（PG18 `\restrict` / `OR REPLACE`；logs 用 replica role）。对账：`accounts` / `usage_billing_dedup` / `usage_logs`。

**Cutover：** Porkbun A→NEW_IP；单域名 `API_DOMAIN`；CIDR=`167.233.211.115/32`；sync caddy；删 `-hz`；stub `https://`；prod pin+restart。

**Drain：** 旧机 `docker stop` app（+ gemini-web）；留 PG/Caddy/Redis；standby；源近窗 usage=0。

**Verify：** `dig +short <host> @8.8.8.8`；`/health`；prod stub smoke；disk-metrics/Feishu/ghcr-prune。

## Prod / LS recreate

- 无卷/跨云：DR §4.4；卷在：优先 §3。  
- LS recreate：expansion + 显式 `recreate=true`。

**回滚：** 正式 A（及 prod pin）改回旧 IP；勿双写。
