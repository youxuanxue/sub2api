# Edge egress IP registry

**现网流量出口：** Hcloud / AWS 分批迁移舰队见 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。
本文件登记 **Lightsail 残留 Static IP**（standby / 污染排除），不是现网路由表。

- **LS 矩阵 IP：** [`deploy/aws/lightsail/edge-targets-lightsail.json`](../../deploy/aws/lightsail/edge-targets-lightsail.json)
- **Exclusion registry：** [`deploy/aws/stage0/edge-polluted-ips.json`](../../deploy/aws/stage0/edge-polluted-ips.json)
- **Enforcement：** [`deploy/aws/stage0/record-polluted-ip.py`](../../deploy/aws/stage0/record-polluted-ip.py) + [`ops/lightsail/rotate-static-ip.sh`](../../ops/lightsail/rotate-static-ip.sh)
- **Regenerate：** `scripts/edge-ip-status.sh --markdown`；`--check` 核对下方 generated 块

Rotation：[`tokenkey-stage0-edge-lightsail-ip-rotation`](../../.cursor/skills/tokenkey-stage0-edge-lightsail-ip-rotation/SKILL.md)。勿把污染表中的 IP 再绑到任何边。

## Lightsail standby Static IPs（非现网写路径）

<!-- BEGIN edge-ip-status:standby (generated from deploy/aws/lightsail/edge-targets-lightsail.json) -->
| Edge | Region | Domain | Static IP name | IPv4 |
| --- | --- | --- | --- | --- |
| `uk1` | eu-west-2 | `api-uk1.tokenkey.dev` | `tokenkey-edge-uk1-aws-20261015-ip` | `18.132.100.198` |
| `uk2` | eu-west-2 | `api-uk2.tokenkey.dev` | `tokenkey-edge-uk2-aws-20261015-ip` | `35.178.162.181` |
<!-- END edge-ip-status:standby -->

2026-10-17 起 uk1/uk2/us4/us5 亦为 standby（`deployable=false`）；`us3`/`us6` 仍仅 gemini drain 前保留。

## Polluted IPs (do not re-use)

<!-- BEGIN edge-ip-status:polluted (generated from deploy/aws/stage0/edge-polluted-ips.json) -->
| IP | Region | Notes |
| --- | --- | --- |
| `3.9.160.161` | eu-west-2 | upstream API risk-block (2026-05-20) |
| `35.177.124.150` | eu-west-2 | upstream API risk-block (2026-05-22) |
| `16.61.87.51` | eu-west-2 | EC2 uk1 orphan EIP released 2026-05-26 after EC2→Lightsail migration (not upstream pollution; exclude from re-allocation) |
| `18.135.59.111` | eu-west-2 | Lightsail edge-uk1 tokenkey-edge-uk1-ls-ip superseded 2026-05-26; released after matrix correction to 13.134.80.182 |
| `100.48.129.133` | us-east-1 | Lightsail edge-us2 StaticIp-2 upstream API risk-block (2026-05-29) |
| `52.47.52.132` | eu-west-3 | EC2 fra1 EIP released 2026-06-01 on EC2 decommission (fra1 goes Lightsail-only; not upstream pollution; exclude from re-allocation) |
| `16.147.170.3` | us-west-2 | EC2 us1 EIP released 2026-06-07 on edge retirement (accounts migrated to us6 Lightsail; cc/codex fingerprint egress migrated to 52.15.35.197; not upstream pollution; exclude from re-allocation) |
| `3.128.102.134` | us-east-2 | Lightsail edge-us3 Static-oh-1 upstream API risk-block (2026-06-16); swapped to vless-oh-fresh-1 / 18.220.195.44; old Static-oh-1 detached, exclude from re-allocation |
| `44.224.133.40` | us-west-2 | Lightsail edge-us5 StaticIp-or-2 upstream API risk-block (2026-06-16); swapped to vless-or-fresh-1 / 32.185.163.163; old StaticIp-or-2 detached, exclude from re-allocation |
| `13.134.80.182` | eu-west-2 | Lightsail edge-uk1 tokenkey-edge-uk1-ls-ip upstream API risk-block (2026-06-16); swapped to vless-uk-fresh-1 / 18.175.27.120; old static IP released, exclude from re-allocation |
| `32.185.163.163` | us-west-2 | upstream-api-risk-block-2026-08-28; static_ip_name=vless-or-fresh-1; released via ops/lightsail/rotate-static-ip.sh |
| `3.148.79.145` | us-east-2 | upstream-api-risk-block-2026-08-28; static_ip_name=StaticIp-oh-3; released via ops/lightsail/rotate-static-ip.sh |
| `35.81.204.18` | us-west-2 | upstream-api-risk-block-2026-08-28; static_ip_name=Static-or-1; released via ops/lightsail/rotate-static-ip.sh |
| `18.220.195.44` | us-east-2 | upstream-api-risk-block-2026-08-28; static_ip_name=vless-oh-fresh-1; released via ops/lightsail/rotate-static-ip.sh |
<!-- END edge-ip-status:polluted -->
