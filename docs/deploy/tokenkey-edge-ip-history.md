# Edge egress IP exclusion registry

Permanent list of **excluded** edge egress IPs (EC2 EIP and Lightsail Static IP). Do not bind these addresses to any edge again.

- **Source of truth:** [`deploy/aws/stage0/edge-polluted-ips.json`](../../deploy/aws/stage0/edge-polluted-ips.json)
- **Enforcement:** [`deploy/aws/stage0/allocate-clean-egress-eip.py`](../../deploy/aws/stage0/allocate-clean-egress-eip.py) (EC2 `rotate_egress_ip` path); [`deploy/aws/stage0/record-polluted-ip.py`](../../deploy/aws/stage0/record-polluted-ip.py) + [`ops/lightsail/rotate-static-ip.sh`](../../ops/lightsail/rotate-static-ip.sh) (Lightsail Static IP rotation)
- **Regenerate table:** `scripts/edge-ip-status.sh --markdown` (paste polluted block below if it drifted)

Active edge EIP rotation runbook: [`.cursor/skills/tokenkey-stage0-edge-ip-rotation/SKILL.md`](../../.cursor/skills/tokenkey-stage0-edge-ip-rotation/SKILL.md).

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
<!-- END edge-ip-status:polluted -->
