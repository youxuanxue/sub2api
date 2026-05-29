# Edge egress IP exclusion registry

Permanent list of **polluted** EC2 egress IPs. Do not bind these addresses to any edge again.

- **Source of truth:** [`deploy/aws/stage0/edge-polluted-ips.json`](../../deploy/aws/stage0/edge-polluted-ips.json)
- **Enforcement:** [`deploy/aws/stage0/allocate-clean-egress-eip.py`](../../deploy/aws/stage0/allocate-clean-egress-eip.py) (EC2 `rotate_egress_ip` path)
- **Regenerate table:** `scripts/edge-ip-status.sh --markdown` (paste polluted block below if it drifted)

Active edge EIP rotation runbook: [`.cursor/skills/tokenkey-stage0-edge-ip-rotation/SKILL.md`](../../.cursor/skills/tokenkey-stage0-edge-ip-rotation/SKILL.md).

## Polluted IPs (do not re-use)

<!-- BEGIN edge-ip-status:polluted (generated from deploy/aws/stage0/edge-polluted-ips.json) -->
| IP | Region | Notes |
| --- | --- | --- |
| `3.9.160.161` | eu-west-2 | upstream API risk-block (2026-05-20) |
| `35.177.124.150` | eu-west-2 | upstream API risk-block (2026-05-22) |
| `100.48.129.133` | us-east-1 | Lightsail edge-us2 StaticIp-2 upstream API risk-block (2026-05-29) |
<!-- END edge-ip-status:polluted -->
