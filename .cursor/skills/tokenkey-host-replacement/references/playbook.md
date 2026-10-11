# Host replacement — 命令骨架

现网与卷恢复入口：`deploy/hetzner/README.md`；AWS DR 仅用于 standby / AWS 离机备份读取。纪律见上级 `SKILL.md`。

## Edge → Hetzner

**Provision：** `deploy/hetzner` render → cloud-init → SSM Hybrid → dispatch。

**Replicate：** 源 `tokenkey-pgdump.sh`（precious）+ logs data-only → S3；目标 restore；对账 `accounts` / `usage_billing_dedup` / `usage_logs`。

**Cutover：** Porkbun A→NEW_IP；单域名 `API_DOMAIN`；CIDR 取 Hetzner README；sync caddy；stub `https://`；prod pin+restart。

**Drain：** 旧机停 app（+ gemini-web）；留 PG/Caddy/Redis ≥7d。

**Verify：** `dig +short <host> @8.8.8.8`；`/health`；smoke；disk-metrics/Feishu/ghcr-prune。

## Prod / AWS standby recreate

- hcloud prod：按 Hetzner README 的恢复入口核对并保留 Volume；无卷/跨云先核验离机 dump 和密钥恢复点。
- AWS standby：仅显式恢复旧机时使用 AWS DR 的 EBS/CFN 或 Lightsail expansion；不得对现网套用。

**回滚：** 正式 A（及 prod pin）改回旧 IP；勿双写。
