# spec-delta: edge host 外探告警 + Edge 主机内存/磁盘 Feishu

## Background

2026-08-03 edge-us6 guest hang：SSM `ConnectionLost`、`NetworkOut=0`、公网 443 超时约 16h。
App 内 Ops/Feishu 无法在主机僵死时发出；prod failover 掩盖单 edge 失联；
2026-07 退役的 `edge-health-watch` 与 Edge 缺失 prod 级 host mem-guard 共同造成静默。

## Delta

### ADDED

- 恢复 `.github/workflows/edge-health-watch.yml`（~15min，状态差飞书）。
- `ops/observability/edge_https_health.py`：外网 `GET /health`；`reachable`=传输成功，`healthy`=HTTP 200。
- Edge `tokenkey-disk-metrics-edge.sh` 增加与 prod 同构的 **memory-pressure** Feishu（MemAvailable%）。

### MODIFIED

- `scan-edge-health.sh`：仅 HTTPS **传输失败**（timeout/connect，`http_code=0`）直接 `unreachable`（`reason=https_unreachable`）并跳过 SSM；非 200（部署期 502/503）仍走 SSM。
- HTTPS helper / target resolver / verdict 集合不完整属于 watch 自身失败：保留 SSM 证据但整轮非零退出，不伪报 edge 故障，也不推进 dedup state。
- Feishu 仅在响应 body 确认成功后提交新 dedup key；缺配置、投递拒绝或 transport failure 均硬失败并保留旧 key，下一轮继续重试。
- `sync-edge-host-units-via-ssm.sh`：推送/描述覆盖 disk+memory；部署后仍由此脚本 backfill。

### REMOVED

- （无）此前对 edge-health-watch 的“退役”决定被本 delta 撤销。

## Scenarios

1. **正向**：edge 主机 hang / 443 不通 → watch 一轮内 actionable 含 `unreachable` → 飞书红卡。
2. **正向**：Edge MemAvailable 塌陷 ≥90% 且 webhook 已同步 → 5min timer 发内存压力卡（主机仍可达时）。
3. **负向**：仅 leading `down`（池空但无 429）→ 不 page（沿用既有 dedup 规则）。
4. **负向**：Feishu 返回 HTTP 200 但 body `code != 0` → 不提交 key，workflow 失败，后续轮次继续尝试。
5. **负向**：HTTPS helper 崩溃 / 非 JSON 或 scan 结果缺 edge → 不做 alert/recovery 决策，workflow 失败。
6. **回归**：decision / HTTPS probe / delivery / scan integration / host pressure selftests 与 workflow contract 全绿。

## Validation

```bash
python3 ops/observability/edge_https_health.py --selftest
python3 ops/observability/edge-health-alert.py --selftest
python3 ops/observability/edge_health_delivery.py --selftest
bash ops/observability/test_scan_edge_health.sh
bash deploy/aws/lightsail/tokenkey-disk-metrics-edge.sh --selftest
python3 scripts/checks/edge-health-watch-contract.py
# backfill live edge (example us6):
# AWS_REGION=us-east-2 bash ops/stage0/sync-edge-host-units-via-ssm.sh mi-...
gh workflow run edge-health-watch.yml -f dry_run=true
```
