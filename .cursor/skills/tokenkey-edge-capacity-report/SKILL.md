---
name: tokenkey-edge-capacity-report
description: Read-only TokenKey edge capacity report refresh from retained access, usage, and error logs. Use when recalculating sustained clean account concurrency (C1/C10), grouping accounts by platform+channel_type, explaining F/H evidence, or updating docs/ops/edge-capacity-report-20260720-c1.md across deployable edges.
---

# TokenKey Edge 容量报告

用确定性脚本刷新账号级与同类型账号的持续无错并发证据。该流程是 `tokenkey-online-traffic-profile` 的容量报告专用入口。

## 只读边界

- 只允许 `SELECT`、只读 SSM probe 和本地报告写入。
- 禁止修改账号、cap、调度配置、Redis、服务进程或部署状态。
- 禁止手写临时 SQL 重算、按最近时间猜配 usage/access、手工修改生成报告。
- 需要应用推荐值时，停止在报告阶段，另行取得用户明确授权。

## 标准刷新

先运行离线行为测试：

```bash
python3 -m unittest ops.observability.test_edge_capacity_report -v
```

再刷新所有当前 deployable Edge：

```bash
python3 ops/observability/edge_capacity_report.py collect \
  --edges auto \
  --days 60 \
  --min-seconds 60 \
  --raw-dir .cache/edge-capacity-report-raw \
  --output docs/ops/edge-capacity-report-20260720-c1.md
```

本地 `edge_capacity_report.py` 负责 SSM 编排、同类型聚合和 Markdown；远端 `edge_capacity_probe.py` 只负责只读 SQL、错误归属与持续区间重建。目标由 `resolve-edge-target.py --list-deployable` 解析，`run-probe.sh` 在等待预算内只轮询原 command id，不重复提交。

只重绘已有 JSON 时使用：

```bash
python3 ops/observability/edge_capacity_report.py render \
  --raw-dir .cache/edge-capacity-report-raw \
  --output docs/ops/edge-capacity-report-20260720-c1.md
```

`render` 会按 deployable Edge SSOT 自动加载并校验完整集合，Edge 增减时不会静默沿用旧列表。`.cache/edge-capacity-report-raw/` 只用于本地审计与离线重绘，保持在 git ignore 中，不提交账号快照和原始日志聚合结果。

## 固定口径

- `F`：精确 request id 关联后，以 access 完成时间锚定 `usage.duration_ms`；是最终账号 Forward/槽占用的下界，也是推荐依据。
- `H`：完整 HTTP lifecycle；包含等待、路由、retry/failover 和收尾，是上界参考。F/H 的各自最大值不保证同时间发生，禁止直接相减。
- `Observed`：一个 pristine 最大连续区间达到持续门槛。
- `Repeated`：至少三个独立最大连续区间达到门槛；一段长区间只算一次。
- `Cross-day`：Repeated 的区间开始日期覆盖至少两个 UTC 日期。
- `Pristine`：无最终对客失败，也无归属于该账号的 recovered upstream/account-auth 事件。
- 错误先通过稳定 request id 关联最终 access；关联后仍无法归属账号的池级错误不摊给账号。
- collect 固定一个共同绝对 `snapshot_at`；各 Edge 的实际 access 起点必须覆盖整个窗口，请求窗口、无错窗口、持续门槛和落稳时间必须完全一致，否则拒绝合并。
- 同类型建议：按 `(platform, channel_type)` 分组；样本不少于三个时，取至少三个独立账号且跨至少两个 Edge 证明的最高 F pristine Cross-day 值。两个样本必须分属两个 Edge 并取共同证明值，一个样本只标暂定。
- 当前 cap 只是本地调度快照，不作为同类型上游能力上限；建议值只由实际 F pristine Cross-day 证据决定。
- probe 不采集账号名、邮箱、credentials 或模型明细；报告和 raw JSON 都只用 `Edge + account_id` 标识账号。

## 完成检查

1. 确认每个目标输出 `status=success`，报告包含所有 deployable Edge。
2. 确认报告中的日志采样、F 关联率、无效 duration 和共同留存窗口没有异常；证据不足时保留限制，不外推。
3. 运行：

```bash
python3 -m unittest ops.observability.test_edge_capacity_report -v
git diff --check
```

4. 若要提交 PR，按项目规则运行 `./scripts/preflight.sh`，PR body 使用中文并从实际 commit 集合生成。

报告是生成物。SQL/区间口径改 `edge_capacity_probe.py`，聚合/文案改 `edge_capacity_report.py`；两者都必须先补行为测试，再重新 collect/render。
