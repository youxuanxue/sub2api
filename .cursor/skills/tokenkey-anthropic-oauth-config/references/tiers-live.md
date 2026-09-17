## 2.5 tier 值实时下发（check → apply-tiers-live → verify，不发版）

**场景**：tier baseline 改动**已合并进 git**（如 PR #604/#629 改 L1–L5 的 concurrency / caps），想**立即**铺到全 fleet，不等下次发版。`check` 会把这种「live 旧值 vs git 新值」报成 `tier_table_drift`（每节点每 tier）。此时用 **`apply-tiers-live`** 把 git 值实时下发。

**与 §3 的关系**：这**不是** §3 里那些废弃的 `plan-tier-bump` 等 escape hatch。tier **数值**仍以 git baseline JSON 为单一源；`apply-tiers-live` 只是把**已在 git 的值**确定性地铺到运行库，落库状态与 admin UI ApplyTier 完全一致，且与下次发版幂等收敛（embed == 同一份 JSON，由 `check-tier-baseline-embed.py` 钉死）。reconciler 仍对单账号 tier 字段漂移 report-only——本工具是 operator 显式触发的 fleet 下发，不抢 reconciler 的活。

**硬约束（顺序不能错）**：

1. **新值必须先合并进 git**。运行时 8 个 cap 从本机 `tiers` 表 overlay（账号 extra 为 null，#472），`apply-tiers-live` 读**磁盘上的 git JSON**。若推未提交的工作区值，下次发版 `ensureSeededFromBaseline` 用 embed(=已合并值)盖回 → live 改动丢失。**tier 值改动走独立小 PR 合并后再 apply。**
2. **直连 SQL over SSM，复刻后端三个副作用**（与 admin handler 一致）：① `tiers` 表 UPDATE（11 strategy 列，**不碰** tls_profile）+ Redis `DEL tiers`/`PUBLISH tiers_updated refresh`（= `TierService.invalidateAndNotify`）；② `accounts.concurrency` UPDATE + `scheduler_outbox('account_changed')` INSERT（worker 1s 内 re-read DB 重建快照）；③ **operator(users.id=1) Σ 同步**（同事务，= sanctioned tier-apply 注入的 `render_admin_operator_concurrency_sync_sql`，立即对齐不靠 reconciler 时序）。concurrency 不在 tier_table_drift 比对键（只比 8 cap），故 verify 单独断言 tiers/账号 concurrency==git。
3. **不用 admin-API**：运行镜像无 curl（busybox wget 不能 PUT）、admin 鉴权未 bootstrap；psql+redis-cli 镜像里就有且本脚本族已在用。

**一条龙命令**：

```bash
JOBDIR="$CLAUDE_JOB_DIR"
MGR=ops/anthropic/manage-anthropic-config.py

# 0) 先确认 git main 已含目标 tier 值（独立 PR 已合并）
python3 -c "import importlib.util as u;s=u.spec_from_file_location('m','$MGR');m=u.module_from_spec(s);s.loader.exec_module(m);print(m._load_expected_tiers()['l5'])"

# 1) check：报 tier_table_drift（live 旧 vs git 新）
python3 $MGR snapshot --out $JOBDIR/snap-pre.json
python3 $MGR check --snapshot $JOBDIR/snap-pre.json        # exit 1 + tier_table_drift items

# 2) apply：实时下发 git 值到全 deployable edge + prod
python3 $MGR apply-tiers-live --snapshot $JOBDIR/snap-pre.json \
  --confirm yes-apply-tiers-live-from-git --job-dir $JOBDIR --json
#   选项：--edges uk1,us7  限定范围；--skip-prod；--no-concurrency 只改 tiers 表+缓存

# 3) verify：必须传【重新抓的】snapshot（pre 的反映不了 apply 后状态）
python3 $MGR snapshot --out $JOBDIR/snap-post.json
python3 $MGR apply-tiers-live --snapshot $JOBDIR/snap-post.json --verify-only --json
#   期望 clean=True：tier_table_drift==0 且 tiers/accounts concurrency==git
python3 $MGR check --snapshot $JOBDIR/snap-post.json        # 独立第三方确认：any_violation=False + redis_cache_drift 干净
```

**确认码**：`--confirm yes-apply-tiers-live-from-git`（区别于级联 `CONFIRM_CODE`，防误粘）。

**残留风险**：live 写、非发版。值扛过 tier 缓存 pub/sub、scheduler outbox、5min 全量 rebuild；唯一回滚点是**下次发版前某节点在旧镜像上重启**（tiers 行被旧 embed reseed 回去；`accounts.concurrency` 列不被 tier seeding 重写，扛过重启）。不静默——下次 `check` 重报 `tier_table_drift`。**根治 = 下次例行发版**（embed==git，幂等 reseed 同值，重启 durable）。本工具是「立即生效」桥，发版是「永久固化」。
