# Edge egress IP registry

Live Lightsail Static IPs and the permanent exclusion list of polluted /
retired egress addresses. Do not bind excluded addresses to any edge again.

- **Live IPs:** [`deploy/aws/lightsail/edge-targets-lightsail.json`](../../deploy/aws/lightsail/edge-targets-lightsail.json) (`porkbun_a_ipv4` / `static_ip_name`)
- **Exclusion registry:** [`deploy/aws/stage0/edge-polluted-ips.json`](../../deploy/aws/stage0/edge-polluted-ips.json)
- **Enforcement:** [`deploy/aws/stage0/record-polluted-ip.py`](../../deploy/aws/stage0/record-polluted-ip.py) + [`ops/lightsail/rotate-static-ip.sh`](../../ops/lightsail/rotate-static-ip.sh)
- **Regenerate tables:** `scripts/edge-ip-status.sh --markdown` (current + polluted); `--check` fails if either block drifted

Rotation runbook: [`.cursor/skills/tokenkey-stage0-edge-lightsail-ip-rotation/SKILL.md`](../../.cursor/skills/tokenkey-stage0-edge-lightsail-ip-rotation/SKILL.md).

The EC2 `rotate_egress_ip` path was removed 2026-06-07 when edges went Lightsail-only; legacy EC2 EIP rows stay in the polluted table as history.

Prod talks to edges by hostname (`api-<id>.tokenkey.dev`). After an A-record change, AWS VPC DNS (`10.0.0.2`) can keep the old IP until the Porkbun TTL expires; `/health` from a public resolver can be green while prod still times out.

## Current live Static IPs

<!-- BEGIN edge-ip-status:current (generated from deploy/aws/lightsail/edge-targets-lightsail.json) -->
| Edge | Region | Domain | Static IP name | IPv4 |
| --- | --- | --- | --- | --- |
| `us3` | us-east-2 | `api-us3.tokenkey.dev` | `vless-oh-fresh-1-rot-20260828T122454Z` | `18.216.113.132` |
| `us4` | us-west-2 | `api-us4.tokenkey.dev` | `Static-or-1-rot-20260828T122052Z` | `32.188.80.151` |
| `us5` | us-west-2 | `api-us5.tokenkey.dev` | `vless-or-fresh-1-rot-20260828T115642Z` | `16.144.175.131` |
| `us6` | us-east-2 | `api-us6.tokenkey.dev` | `StaticIp-oh-3-rot-20260828T121745Z` | `3.147.98.112` |
<!-- END edge-ip-status:current -->

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
