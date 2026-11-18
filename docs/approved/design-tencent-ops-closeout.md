---
title: 腾讯运维旁路与 SecureString 完整收尾处置清单
status: approved
approved_by: "feng（2026-11-18 会话：同意建议。继续。）"
approved_at: 2026-11-18
risk: high
authors: [agent]
created_at: 2026-11-18
parent: docs/approved/design-tencent-ops-control-plane.md
---

# 腾讯运维旁路与 SecureString 完整收尾处置清单

## 用户意图（2026-11-18）

在门牙 + `ghcr-prune` 首迁之后，把纪律 **1（管腾讯机 = TAT）** 与 **2（ops 密钥 ≠ SecureString）** 完整收尾；无用 workflow / 远程路径可删。

本文件是**处置审批基线**（不是再发明第三套产品）。父文完成定义不变；Hybrid/OIDC **注销**仍另批。

## 完成定义（本收尾）

1. allowlist 三表缩到只剩：**显式 break-glass** + **只碰 AWS 资源/协议的合法入口**（若有）。  
2. 对腾讯机的**日常** GHA / CLI 默认路径无 `configure-aws-credentials`。  
3. 活 ops 密钥 SSOT = `tencent-ops` secret / 主机 root-only / 腾讯 COS；一次不读 AWS 的装机或恢复证据后，删除 SecureString 参数与写脚本。  
4. `deploy/hetzner/README.md` 与 allowlist 表述一致。  
5. 门牙继续：allowlist **只许缩不许扩**。

不宣称：Hybrid 注销、Oregon/GHCR/供应商 AWS 清零。

## 执行原则

| 原则 | 说明 |
|---|---|
| 先公共搬运工 | 先让 `run-probe` / prod 远程执行日常默认 TAT（对齐 cn `deploy/domestic/remote_exec.py`），多数 workflow 只改 environment + 删 OIDC |
| 同型复用 | 旁路迁法对齐已上线的 `sync_ghcr_prune_timer.py` + `tencent-ops` |
| 能删不迁 | 退役入口、零调用、已被 rolling/pgBackRest 取代的，直接删，不迁 TAT |
| 禁止大爆炸 | 可多 PR，但每 PR 必须缩 allowlist；本清单一次批完处置，不逐条再问 |
| break-glass | 默认零 AWS；确需保留的 SSM 壳改名/注释为 LEGACY，不进日常 workflow |

## A. Workflow 处置（审批勾选）

证据：2026-11-18 `gh run list`（最近 1–2 次）。

| # | Workflow | 近况 | 建议处置 | 批 |
|---|---|---|---|---|
| A1 | `prod-feishu-config-sync.yml` | 日更成功（11-17） | **迁 TAT**（`tencent-ops`）；去掉 OIDC | ✅ |
| A2 | `prod-feishu-config-repair.yml` | 从未跑过 | **删除**（能力并入 sync 的显式 input，或保留单一 sync） | ✅ |
| A3 | `ops-daily-diagnostics.yml` | 定期成功 | **迁 TAT**（依赖 B1 `run-probe`） | ✅ |
| A4 | `ops-cn-backup-watch.yml` | 定期成功 | **迁 TAT**（cn-prod-data） | ✅ |
| A5 | `ops-payment-billing-watch.yml` | 定期在跑（近失败） | **迁 TAT**（经 `run-probe`）；修失败不在本清单范围但通道要换 | ✅ |
| A6 | `ops-kiro-monthly-recovery.yml` | 无 run 记录（月末才触发） | **迁 TAT**；保留月末 schedule | ✅ |
| A7 | `client-fidelity-watch.yml` | 日更在跑（近失败） | **迁 TAT**（`run-probe`） | ✅ |
| A8 | `distribute-cn-image.yml` | 有成功 | **迁 TAT**：`publish-from-prod` 的 `run-probe` 段；腾讯密钥已在 `tencent-ops` | ✅ |
| A9 | `ops-stage0-container-log-policy.yml` | 从未跑过 | **迁 TAT** 或确认 bootstrap 已含策略后 **删除 workflow**（建议：迁一条 one-shot 后删 SSM 壳） | ✅ |
| A10 | `sync-docs-to-pages.yml` | 上次 2026-06 | **删除**（确认 pages 不再由此同步；若仍要，再迁 TAT） | ✅ |
| A11 | `ops-stage0-pg-dump-refresh.yml` | 上次 2026-10-18；pgdump 已退役 | **删除** workflow + `pg_dump_refresh_via_ssm.sh` | ✅ |
| A12 | `ops-pgdump-restore-canary.yml` | 上次 2026-10-19；周定时已摘 | **删除**自动入口；历史 SQL 恢复工具若仍要，改为本地/COS 读材料的手动文档，不再 OIDC→SSM | ✅ |
| A13 | `ops-physical-restore.yml` | 曾 skipped | **迁 TAT** 做验收探针；隔离恢复网络/材料走 COS 或主机，不读 SecureString 活源 | ✅（OIDC 仅 AWS 隔离恢复机；验收探针 `probe-physical-restore-acceptance.sh` + TAT；无 env SecureString） |
| A14 | `deploy-stage0.yml` | 11-15 仍有人 dispatch 成功 | **退役日常入口**：`dispatch-prod-deploy.sh` 拒绝 `legacy-deploy\|replay\|smoke-only`；workflow 正文暂留以过 sentinel/契约测试，后续另 PR 抽壳 | ✅（dispatch） |
| A15 | `pricing-registry-publish.yml` | 活；**未进 oidc allowlist**（门牙漏扫形态） | **迁 TAT** 发到 data 机；收尾时把门牙补到能扫到「OIDC + 调用 allowlisted/SSM 脚本」或显式路径引用 | ✅ |

## B. 远程路径 / 脚本处置

| # | 路径 | 建议处置 | 批 |
|---|---|---|---|
| B1 | `ops/observability/run-probe.sh` + `ops/stage0/ssm_execution.py` | **日常默认 TAT**；`--via ssm` 仅 LEGACY | ✅ |
| B2 | `deploy/domestic/remote_exec.py` | 已默认 TAT → **保留**；确认无新默认 SSM | ✅ |
| B3 | `deploy/domestic/deploy_via_ssm.sh` | **迁 TAT** 包装或改调 `remote_exec`；文件可改名去掉 via-ssm | ✅（pull/put-file → `remote_exec` 默认 TAT） |
| B4 | `ops/stage0/sync-feishu-config.sh` | **迁 TAT**（服务 A1） | ✅（默认 TAT→data；`deploy-stage0` 显式 `TOKENKEY_REMOTE_VIA=ssm`） |
| B5 | `ops/stage0/sync-container-log-policy-via-ssm.sh` | **迁 TAT** 或随 A9 删除 | ✅（日常改 `probe-sync-container-log-policy.sh`+TAT；via-ssm 留 LEGACY） |
| B6 | `ops/stage0/sync_docs_pages.sh` | 随 A10 **删除** | ✅ | <!-- script-ref-allow-missing: tombstone -->
| B7 | `ops/stage0/sync_caddyfile_via_ssm.sh` | **迁 TAT**（入口/应用 Caddy 热更） | ✅（`execute_remote_params` 默认 TAT） |
| B8 | `ops/stage0/sync-host-bbr-via-ssm.sh` | **迁 TAT** | ✅ |
| B9 | `ops/stage0/sync-prod-pg-tuning-via-ssm.sh` | **迁 TAT**（数据机）或确认 GUC 已固化后删 | ✅ |
| B10 | `ops/stage0/sync-host-mem-guard-via-ssm.sh` | **删除**（注释写明 Legacy EC2 standby） | ✅ | <!-- script-ref-allow-missing: tombstone -->
| B11 | `ops/stage0/warm_pull_via_ssm.sh` | warm 已 TAT → **删或标 LEGACY**；日常不引用 | ✅ | <!-- script-ref-allow-missing: tombstone -->
| B12 | `ops/stage0/deploy_via_ssm.sh` / `deploy_via_ssm_bluegreen.sh` | **删日常引用**；蓝绿保持 LEGACY 或删（与 A14 一致） | ✅（LEGACY 头；日常 rolling） |
| B13 | `ops/stage0/sync-ghcr-prune-timer-via-ssm.sh` | 已 LEGACY → **保留薄壳或删**（默认已是 TAT） | ✅（LEGACY 薄壳；日常 `sync_ghcr_prune_timer.py`） |
| B14 | `ops/stage0/pg_dump_refresh_via_ssm.sh` | 随 A11 **删除** | ✅ | <!-- script-ref-allow-missing: tombstone -->
| B15 | `ops/stage0/resolve-prod-running-tag-via-ssm.sh` | **迁 TAT** 只读；或并入 rolling 回执 | ✅ |
| B16 | `ops/stage0/assert-live-host-state.sh` / `check_exclusive_group_orphans_via_ssm.sh` / `deploy-error-clustering-binary.sh` | **迁 TAT** 或并入 diagnostics | ✅ |
| B17 | `ops/observability/fetch-gateway-debug-log.sh` | **迁 TAT** | ✅ |
| B18 | `ops/pricing/manage-account-model-mapping-runtime.py` / `manage-overlay-runtime.py`（若打 SSM） | **迁 TAT**（服务 A15） | ✅（经 `ssm_execution` 默认 TAT；`pricing-registry-publish` 钉 `TOKENKEY_REMOTE_VIA=tat` + `PROD_SSM_NODE=data`） |
| B19 | `ops/newapi/audit-model-mapping.py` / `ops/antigravity/check-antigravity-account-config.py` | **迁 TAT** | ✅ |
| B20 | `ops/archive/data_layer_*.py` / `ops/migration/data_layer_partition_maintenance.py` | **迁 TAT**；若已只读且罕用，可改为显式手动 TAT one-shot | ✅ |
| B21 | `ops/stage0/run_physical_restore.py` | 恢复路径去 SecureString 活源；探针 **TAT**（服务 A13） | ✅（无 env SecureString；AMI 公共参数属 AWS 隔离恢复） |

## C. SecureString / 密钥收尾（纪律 2）

| # | 项 | 建议处置 | 批 |
|---|---|---|---|
| C1 | 活参数盘点 | 列出并核对：`/tokenkey/prod/stage0/env-secrets-backup`、`/tokenkey/hetzner/*/stage0/env-secrets-backup`、`/tokenkey/hetzner/*/stage0/pgdump-storage-recovery`、`/tokenkey/prod/stage0/pgdump.b64` 等 | ✅ 代码侧盘点见下方；AWS 控制台逐条核对/删除属 C5 |
| C2 | 新 SSOT | **主机 root-only**（数据机/应用机 `.env` 同源不进 git）+ 离机恢复材料进 **腾讯 COS**（对齐 `design-cos-object-archive.md`）；CI 仅需的用 **`tencent-ops` secret**，不新建密钥产品 | ✅（主机 `.env` 活源；`restore-prod-env-secrets --from-file/--allow-generate`；COS 归档桶既有） |
| C3 | 停写 | 已有门牙；`backup-env-secrets-via-ssm.sh` **删除或改只读迁移工具一次**后移出树 | ✅ tombstone（C5 后永久 exit 2）；`securestring_write_paths=[]` |
| C4 | 停读 | `restore-prod-env-secrets.sh` / bootstrap / `run_physical_restore` / user-data **改为读新 SSOT**；一次装机或隔离恢复证据 | ✅（默认拒 SecureString；bootstrap 不再钉 SSM 参数） |
| C5 | 删参数 | 证据齐全后删除 SecureString；allowlist `securestring_write_paths`→`[]` | ✅（2026-11-18 已删 C1 清单参数；receipt `docs/ops/reports/securestring-c5-delete-20261118T084419Z.txt`；写脚本 tombstone） |

### C1 代码侧参数/读者盘点（2026-11-18）

| 参数名（或模式） | 写路径 | 读路径 | 备注 |
|---|---|---|---|
| `/tokenkey/prod/stage0/env-secrets-backup` | `backup-env-secrets-via-ssm.sh`（现默认拒写） | `restore-prod-env-secrets.sh`、`run_physical_restore.py`、disaster-recovery RUNBOOK | 活 prod 恢复材料；C4 改 COS/主机后再删 |
| `/tokenkey/hetzner/*/stage0/env-secrets-backup` | 历史 Hetzner 路径 | 同上系文档 | Hetzner 已退役；C5 一并清 |
| `/tokenkey/hetzner/*/stage0/pgdump-storage-recovery` | 历史 pgdump | 退役文档 | pgdump 已退役 |
| `/tokenkey/prod/stage0/pgdump.b64` | 历史 pgdump | 退役文档 | pgdump 已退役 |

## D. 明确不做（再确认）

| 项 | 理由 |
|---|---|
| 注销 Hybrid / 拆 OIDC 角色 | 父文：默认消费者清零后另批 |
| 迁 Oregon / GHCR / 供应商 AWS | 父文不在范围 |
| 改拓扑 / 重开蓝绿 | 父文砍掉 |
| 把 break-glass 写回产品默认 | 否 |

## E. 建议落地顺序（批准后执行，不另批）

1. **公共 TAT**：B1（+ 补门牙扫到 A15）  
2. **删退役**：A10–A12、A14、B10、B11、B14 + 缩 allowlist  
3. **高频旁路**：A1/A3/A4/A8 + 对应 B  
4. **其余 watch / sync**：A5–A7、A9、A13、B 余下  
5. **密钥 C1–C5**（可与 3–4 并行，但删参数必须在停读证据之后）  
6. README + allowlist 清空至只剩 LEGACY（若仍留）

## 审批方式

请直接在本文件改处置（迁/删/留）、把需要改口的行改掉，然后将 `approved_by` / `status` 改为批准态并合并；**合并本文件 = 批准整张清单**。批准前不开始生产迁移动作（只允许继续准备草案 PR）。
