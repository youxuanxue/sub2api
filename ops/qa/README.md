# QA Operations

QA is raw-only. Production capture is sealed, archived to the private raw object
store, restore-verified, and cleaned by `tokenkey-qa-maintenance.timer`.

The lifecycle owner is `deploy/aws/stage0/tokenkey-qa-maintenance.sh`; policy is
`ops/qa/policy.yaml` and rollout state is `ops/qa/deploy_rollout.yaml`. Edge
capture and archive remain disabled. Raw records are never exposed through the
product API or user interface.

The approved migration is documented in
[`docs/approved/qa-raw-only.md`](../../docs/approved/qa-raw-only.md). Migration
steps are offline copy, final incremental copy, per-object checksum comparison,
restore verification, and an independently reviewed maintenance coordinate
switch. AWS source data remains retained until the observation period and a
separate deletion approval.

Useful checks:

```bash
python3 scripts/checks/qa-lifecycle-ssot.py --self-test
python3 scripts/checks/qa-lifecycle-ssot.py
python3 -m unittest ops.stage0.test_prod_component_release
```

Raw 生命周期的保留运维入口：

| 操作 | Owner | 条件 |
| --- | --- | --- |
| 单一 owner 计划与激活 | `ops/stage0/activate-qa-single-owner-via-ssm.sh` | 先 plan；核实 capture seal、restore 与 DROP pause，再使用该 plan hash 的明确确认令牌 activate |
| 暂停 DROP 与读取维护 pin | `ops/stage0/prod_release_state.py` | 仅独立迁移/接受验证，普通 gateway 发布不调用 |
| 定时维护发布 | `ops/stage0/sync-qa-maintenance-timer-via-ssm.sh` | 独立维护 pin 与受限子容器，健康验收通过后才确认配置 |
| 合成 Object Storage 兼容性探针 | `ops/qa/probe_hcloud_object_storage.py` | 显式 opt-in，仅专用合成 bucket；不证明生产迁移完成 |
| raw 预复制与冻结清单验证 | `ops/qa/migrate_hcloud_objects.py` | 接口与上线安全边界见 raw-only 契约 |

这些工具不是本 PR 的线上执行清单；单一 owner 激活和存储切换分别验收。
