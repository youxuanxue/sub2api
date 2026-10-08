---
name: tokenkey-plan-account-usage-report
description: >-
  Read-only TokenKey plan-account metering report for VolcEngine Agent Plan, Ali
  Token Plan, Qianfan Token Plan, and NVIDIA Build (prod + deployable edges). Use
  when reviewing monthly/period usage, max 5h/7d tile costs, peak RPM/TPM, or
  regenerating docs/ops/plan-account-usage-YYYYMM.md.
---

# TokenKey：Plan 账号周期计量报告

只读刷新四类套餐账号在指定周期内的计量/峰值报告。脚本负责 SSM probe、聚合与 Markdown；模型只解释结果，不手写 SQL 重算。

## 只读边界

- 只允许 `SELECT`、只读 `run-probe.sh` 与本地报告写入。
- 禁止改账号、cap、调度、Redis、进程或部署。
- 禁止用临时 SQL 另算一套口径；口径变更改 `probe-plan-account-usage.sh` / `plan_account_usage_report.py`，并补测试。

## 标准刷新（按月）

```bash
python3 -m unittest ops.observability.test_plan_account_usage_report -v

python3 ops/observability/plan_account_usage_report.py collect \
  --month 2026-09 \
  --raw-dir .cache/plan-account-usage-raw \
  --output docs/ops/plan-account-usage-202609.md
```

自定义半开区间（Asia/Shanghai，`period-end` 不含）：

```bash
python3 ops/observability/plan_account_usage_report.py collect \
  --period-start 2026-09-01 \
  --period-end 2026-10-01 \
  --output docs/ops/plan-account-usage-202609.md
```

仅用已缓存 JSON 重绘：

```bash
python3 ops/observability/plan_account_usage_report.py render \
  --raw-dir .cache/plan-account-usage-raw \
  --output docs/ops/plan-account-usage-202609.md
```

本机 `aws/pyexpat` 启动失败时，先 `python3 scripts/checks/check-local-aws-pyexpat.py --apply`。

## 固定口径

- 账号识别（base_url + channel_type）：
  - VolcEngine Agent Plan：ch45 + `ark.../api/plan/v3` 或 `doubao-agent-plan`
  - Ali Token Plan：ch17 + `token-plan.cn-beijing.maas.aliyuncs.com`
  - Qianfan Token Plan：ch46 + `.../tokenplan/personal`
  - NVIDIA Build：ch1 + `integrate.api.nvidia.com`
- **month**：`[period_start, period_end)` 内 `usage_logs` 合计（`total_cost` / `actual_cost`；不做额外 status 过滤）。
- **账号范围**：`created_at < period_end`；周期结束后新建的账号不进表。
- **max5h / max7d**：从 `period_start` 起**非重叠步进**（5h / 7d），只保留完整格子，取 total_cost 最大格；不是 1h/1d 滚动窗。
- **peak_rpm / peak_tpm**：周期内分钟桶峰值；报告里 TPM 用 **million**（/1e6）。
- Edge 列表：`python3 deploy/aws/stage0/resolve-edge-target.py --list-deployable`。
- Markdown **禁止美元符号**（预览会把美元符号当公式弄乱表格）。

脚本已算完的数字，prompt 不要重算。失败（`status!=Success` / 非零退出）如实报告，绝不编数。

## 完成检查

1. unittest 全绿。
2. 报告覆盖 prod + 全部 deployable edges；无账号的环境允许 0 行。
3. 抽查一档 max5h/max7d 窗口长度分别为 5h / 7d，且落在周期内。
4. 若提交：按项目规则跑 `./scripts/preflight.sh`。

## Owner

| 职责 | 文件 |
| --- | --- |
| 远端只读 SQL | `ops/observability/probe-plan-account-usage.sh` |
| 本地编排/渲染 | `ops/observability/plan_account_usage_report.py` |
| 行为测试 | `ops/observability/test_plan_account_usage_report.py` |
| 报告产物 | `docs/ops/plan-account-usage-YYYYMM.md` |
