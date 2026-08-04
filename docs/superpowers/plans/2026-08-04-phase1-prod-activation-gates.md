# 第一阶段生产启用门禁修复 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** 修复 PR #1536 的生产启用阻塞项，且合并代码不触发任何生产写操作。

**Architecture:** observability 工件负责只读诊断；一个共享 Go maintainer 唯一持有分区目标、窗口、创建和覆盖验证，cron 与 server 一次性模式共同调用；固定用途 controller 只向固定生产 stack 发送固定 SSM 命令。容量原型替换正式文件后删除。

**Tech Stack:** Bash、Python unittest、Go 1.26、database/sql、lib/pq、AWS CLI/SSM、GitHub Actions。

## Global Constraints

- 不部署、不发布、不重启，不执行线上命令。
- telemetry 保持禁用，cleanup hold 保持启用。
- 不删除数据、不扩容、不推进 RDS。
- 只读探针使用 default_transaction_read_only=on、lock_timeout=100ms、statement_timeout=2s。
- 一次性维护使用 lock_timeout=100ms、statement_timeout=5s。
- 精确确认词为 tokenkey-prod-partition-maintenance-v1。
- 后端无 Web 影响；后端 commit 写明 no-web-impact。

---

### Task 1: 修复只读保护诊断

**Files:**
- Create: ops/observability/test_probe_data_layer_safety.py
- Modify: ops/observability/probe-data-layer-safety.sh
- Create: ops/observability/data_layer_snapshot_signal.sh
- Create: ops/observability/test_data_layer_snapshot_signal.py
- Modify: .github/workflows/ops-daily-diagnostics.yml
- Modify: ops/observability/test_ops_daily_diagnostics_workflow.py

**Interfaces:**
- safety probe 内部 resolve_app_container 返回一个 running 容器或失败。
- data_layer_snapshot_signal.sh REGION STACK 输出 latest_snapshot_at JSON。

- [x] **Step 1: 写失败测试并确认 RED**

使用 fake docker 实际运行 safety probe。测试 active-color=green、active 目标未运行、唯一 running fallback、零候选、多候选、显式 stopped 容器。多候选必须得到：

    TELEMETRYSTATS {"probe_ok":false,"enabled":null}

运行：

    python3 ops/observability/test_probe_data_layer_safety.py -v

预期因当前固定 inspect tokenkey 而 FAIL。

- [x] **Step 2: 实现容器解析并确认 GREEN**

实现 container_running，active-color 仅接受 blue/green 且目标 running；fallback 只有 running 候选数为 1 时成功。解析失败不读取任意容器 env。

- [x] **Step 3: 写快照目标失败测试并确认 RED**

fake aws 为 stack 返回 vol-data。真实执行 helper 并断言 describe-snapshots 的 filter 是 Values=vol-data；DataVolumeId 缺失时输出 null 且不调用 describe-snapshots。

    python3 ops/observability/test_data_layer_snapshot_signal.py -v

预期 helper 不存在而 FAIL。

- [x] **Step 4: 实现快照 owner、接入 workflow 并确认 GREEN**

helper 只从 CloudFormation OutputKey=DataVolumeId 取卷，查询最新 completed snapshot；任何错误输出 null。workflow 删除 BlockDeviceMappings[0] 路径并调用 helper。

    python3 ops/observability/test_data_layer_snapshot_signal.py -v
    python3 ops/observability/test_ops_daily_diagnostics_workflow.py -v

- [x] **Step 5: 提交**

    git add ops/observability .github/workflows/ops-daily-diagnostics.yml
    git commit -m "fix(ops): make data safety probes fail closed"

---

### Task 2: 晋升有界容量原型

**Files:**
- Modify: ops/observability/test_data_layer_capacity_safety.py
- Modify: ops/observability/probe-data-layer-capacity.sh
- Modify: ops/observability/data_layer_capacity_verdict.py
- Delete: legacy capacity probe prototype copy
- Delete: legacy capacity verdict prototype copy

**Interfaces:** 正式 probe 输出 PGSTATS、PGGROWTH、DFSTATS；正式 verdict 输出 unknown、green、approaching、trigger。

- [x] **Step 1: 测试切到正式文件并确认 RED**

    _PROBE = _DIR / "probe-data-layer-capacity.sh"
    _VERDICT = _DIR / "data_layer_capacity_verdict.py"

移除“prototype 未接线”断言，保留真实脚本执行、growth timeout unknown、catalog 缺失 unknown、非法数值 unknown。

    python3 ops/observability/test_data_layer_capacity_safety.py -v

- [x] **Step 2: 原型行为替换正式实现并确认 GREEN**

移入 PGOPTIONS、叶分区大小、pg_stat_user_tables 估算、单次 30 天增长查询和 fail-closed 解析。

    python3 ops/observability/test_data_layer_capacity_safety.py -v
    python3 ops/observability/data_layer_capacity_verdict.py --selftest

- [x] **Step 3: 删除 prototype 并提交**

    git grep -n "data-layer-capacity-prototype\|data_layer_capacity_verdict_prototype" -- .
    git add -A ops/observability
    git commit -m "fix(ops): bound the active capacity probe"

---

### Task 3: 提取共享 Go 分区 maintainer

**Files:**
- Create: backend/internal/pkg/partitionmaintenance/maintenance.go
- Create: backend/internal/pkg/partitionmaintenance/maintenance_test.go
- Modify: backend/internal/service/ops_cleanup_executor.go
- Modify: backend/internal/service/ops_cleanup_service.go
- Modify: backend/internal/service/ops_cleanup_service_test.go

**Interfaces:**

    type Mode uint8
    const (
        ModeAllowUnpartitioned Mode = iota
        ModeRequireAllPartitioned
    )
    type TableResult struct {
        Table string
        RangeCount int
    }
    type Result struct {
        Tables []TableResult
    }
    func Ensure(
        ctx context.Context,
        db pgpartition.DB,
        now time.Time,
        mode Mode,
    ) (Result, error)

- [x] **Step 1: 写 maintainer 失败测试并确认 RED**

实现 TestEnsureStrictCreatesAndVerifiesAllTargets、TestEnsureStrictRejectsUnpartitionedTarget、TestEnsureAllowUnpartitionedSkipsCompatibilityTarget、TestEnsureRejectsUncoveredOverlap。固定 now=2026-08-04T12:00:00Z，正向结果为两个 range_count=4 的 ops 表和一个 range_count=8 的 usage 表。

    cd backend
    go test -tags unit ./internal/pkg/partitionmaintenance -run TestEnsure -count=1

- [x] **Step 2: 实现唯一 owner 并确认 GREEN**

目标和窗口只在新 package 声明。每张表先 IsPartitioned，再 EnsureMonthly/EnsureDaily，最后用 pg_inherits 与 pg_get_expr 验证每个目标范围被完整覆盖。strict 遇非分区表报错；compat 跳过。

- [x] **Step 3: cron 改用共享 owner**

删除 service 内 opsPartitionMonthsAhead、opsPartitionDaysAhead、ensureOpsPartitions。runScheduled 调用 ModeAllowUnpartitioned，heartbeat job name 使用 partitionmaintenance.JobName。更新 service sqlmock 覆盖验证 expectations。

    go test -tags unit ./internal/pkg/partitionmaintenance ./internal/service \
      -run 'TestEnsure|TestOpsCleanupScheduled' -count=1

- [x] **Step 4: 提交**

    git add backend/internal/pkg/partitionmaintenance backend/internal/service
    git commit -m "refactor(data): share partition maintenance owner" \
      -m "no-web-impact"

---

### Task 4: server 二进制增加一次性模式

**Files:**
- Create: backend/cmd/server/partition_maintenance.go
- Create: backend/cmd/server/partition_maintenance_test.go
- Modify: backend/cmd/server/main.go

**Interfaces:**

    const partitionMaintenanceConfirmation =
        "tokenkey-prod-partition-maintenance-v1"
    func runPartitionMaintenanceCommand(
        ctx context.Context,
        args []string,
        out io.Writer,
        deps partitionMaintenanceDeps,
    ) error

- [x] **Step 1: 写失败测试并确认 RED**

测试错误确认词不 load config/open DB；成功路径设置两个 timeout、调用 strict maintainer、写 ops_partition_maintenance 心跳并输出 deletion_authorized=false；main 必须在 setup/server 前分流。

    cd backend
    go test -tags unit ./cmd/server -run PartitionMaintenance -count=1

- [x] **Step 2: 实现专用连接与命令分流**

独立 FlagSet 解析 --partition-maintenance-once 与 --confirm。openDB 只用 sql.Open("postgres", DSNWithTimezone)，最大连接数 1 并 Ping；禁止 repository.InitEnt。通过 repository.NewOpsRepository(db).UpsertJobHeartbeat 写成功心跳。

- [x] **Step 3: 确认 GREEN 并提交**

    go test -tags unit ./cmd/server ./internal/pkg/partitionmaintenance \
      ./internal/service -count=1
    git add backend/cmd/server
    git commit -m "feat(ops): add guarded partition maintenance mode" \
      -m "no-web-impact"

---

### Task 5: 固定用途写侧 controller

**Files:**
- Create: ops/migration/data_layer_partition_maintenance.py
- Create: ops/migration/test_data_layer_partition_maintenance.py
- Modify: ops/migration/README.md

**Interface:** python3 ops/migration/data_layer_partition_maintenance.py run --receipt PATH --confirm TOKEN。region 固定 us-east-1，stack 固定 tokenkey-prod-stage0。

- [x] **Step 1: 写失败测试并确认 RED**

测试错误确认词零 AWS 调用；parser 不暴露 target/command/script；成功只接受固定 instance 与完整 JSON；异常回执失败关闭；已有 receipt 不覆盖。

    python3 ops/migration/test_data_layer_partition_maintenance.py -v

- [x] **Step 2: 实现固定 SSM 状态机**

固定流程：

    confirm -> describe stack InstanceId -> send one fixed command
    -> poll same CommandId -> require Success -> validate JSON
    -> atomically create receipt

禁止 region、stack、instance、target、script、command 参数；禁止 run-probe；禁止自动 resubmit。远端解析 running active 容器后固定执行：

    sudo docker exec --user 1000:1000 "$APP_CONTAINER" \
      /app/sub2api --partition-maintenance-once \
      --confirm tokenkey-prod-partition-maintenance-v1

- [x] **Step 3: 更新 README、确认 GREEN 并提交**

README 只写命令形状、receipt、二次生产审批和“本 PR 禁止执行”。

    python3 ops/migration/test_data_layer_partition_maintenance.py -v
    git add ops/migration
    git commit -m "feat(ops): add fixed partition repair controller"

---

### Task 6: Sentinel、preflight 与出口验证

**Files:**
- Modify: scripts/sentinels/perf-query-shape.json
- Modify: scripts/preflight.sh

- [x] **Step 1: 更新机械 owner**

cleanup executor sentinel 继续锚定 DropExpired、ListStraddling、reclaim cap；新增 maintainer sentinel 锚定三张目标表、EnsureMonthly、EnsureDaily、strict mode 和覆盖验证。

- [x] **Step 2: preflight 纳入新测试**

    python3 ./ops/observability/test_probe_data_layer_safety.py
    python3 ./ops/observability/test_data_layer_snapshot_signal.py
    python3 ./ops/migration/test_data_layer_partition_maintenance.py

- [x] **Step 3: 聚焦验证**

    python3 ops/observability/test_probe_data_layer_safety.py -v
    python3 ops/observability/test_data_layer_snapshot_signal.py -v
    python3 ops/observability/test_data_layer_capacity_safety.py -v
    python3 ops/observability/test_ops_daily_diagnostics_workflow.py -v
    python3 ops/migration/test_data_layer_partition_maintenance.py -v
    cd backend
    go test -tags unit ./internal/pkg/partitionmaintenance ./internal/service \
      ./cmd/server -count=1

- [x] **Step 4: 完整门禁**

    cd ..
    git diff --check
    ./scripts/preflight.sh

预期全部 PASS 且无 AWS/生产连接。Docker daemon 不可用导致的既有 Caddy skip 如实记录。

- [x] **Step 5: 提交门禁更新**

    git add scripts/sentinels/perf-query-shape.json scripts/preflight.sh
    git commit -m "test(ops): gate partition activation safety"

- [ ] **Step 6: 最终 review 与 PR**

读取 verification-before-completion 与 xj-review，重跑新鲜 preflight，完成 xj-review 并修复所有阻塞项。用 git log --oneline origin/main..HEAD 重写中文 PR body 与最新提交锚点，然后 push 并创建 PR。禁止部署、运行 controller 或修改线上配置。
