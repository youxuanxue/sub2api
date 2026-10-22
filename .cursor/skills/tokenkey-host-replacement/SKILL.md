---
name: tokenkey-host-replacement
description: >-
  TokenKey prod 换机/整机迁移与跨云恢复演练：pgBackRest 全量/增量/WAL、
  隔离验收、standby 追赶、单写切流与旧机保留。Not image-only deploy or IP rotation.
---

# TokenKey：新机迁移与隔离恢复

现网：[舰队入口](../../../deploy/hetzner/README.md)（路径名历史遗留；活舰队是腾讯美西）。
流程：[pgBackRest 迁机工作流](../../../ops/stage0/pgbackrest-host-migration.md)。
备份/恢复：[physical-backup.md](../../../ops/stage0/physical-backup.md)。
切流后交接：[腾讯迁机交接](../../../docs/ops/tencent-migration-handoff.md)。

输入 `target=prod`、目标云/地域/架构、`phase=plan|rehearse|prepare|cutover|verify`。
默认先 plan；已授权阶段直接推进。腾讯云演练不等于授权创建付费资源或正式切流。
仅镜像部署用 `tokenkey-stage0-release-rollout`；cn-prod 未验收物理备份，不套用 prod 的退役决策。

## 正式迁机从第一天规划（硬）

新机一旦可能成为生产主，**从 `plan` 起就按正式迁机终态设计**，不要「先切流再慢慢补」。  
`rehearse`/`prepare` 可以隔离、不出站、不改 DNS，但路径、容器身份、备份前缀、控制面 glue、告警、发版契约必须对准终态；禁止用临时布局/临时 SSOT 当切流闭环。

`plan` 必须一次性写清并排进同一切流窗（或窗前无感可做项），避免切后再开多轮返工：

| 终态块 | plan 必须写明 |
|---|---|
| DB / WAL | standby→promote、最终 LSN、新 timeline 独立备份前缀、旧前缀只读保留 |
| 布局 / 发版 | 切流结束即可 `deploy-stage0`（见下表）；无 post-cutover 布局停机 |
| 控制面 | `resolve --target auto`→新主；GitHub `PROD_SSM_TARGET=auto`；旧身份失败关闭；`ssm_execution` 等 glue 显式新平台分支 |
| 备份 / 告警 | `RuntimeDirectoryPreserve=yes`；identity/backup/archive-check timer；飞书宿主告警接到新机 |
| DNS / TTL | 切前降 TTL、切后恢复到提供商允许最小值；全部正式名公共 resolver + `curl --resolve` |
| 出口 | 主机切流 ≠ 出口切流；是否同窗迁 `proxy_id`、回滚清单 |
| 契约 / SSOT | unlock、舰队 README、handoff、cutover 清单在 cutover **当日**改到现态 |
| verify | fence/usage、上游 vs 平台错误、`archive_check`、代理分布——不只 `/health` |

2026-10-22 教训：切流后才补布局窗、钉死 `PROD_SSM_TARGET`、漏 glue 分支、飞书/契约滞后 = 反复停机与文档漂移。历史踩坑见 [playbook](references/playbook.md)。

## 判断与不变量

- 先核实备份、连续 WAL 和最新/历史恢复证据；镜像、架构、PG 主版本和恢复参数兼容。
- 演练只写新盘，不挂生产卷，不运行真实业务写者/外部回调，不改 DNS。
- 现有恢复工具是自动清理的验收工具，不能拿来安装持久目标库。
- 正式切换：standby 追 WAL → 源写入隔离 → 最终 WAL/LSN 入仓与重放确认 → 目标 promote → DNS。
  默认只读 ≠ 完整写入隔离；严禁双写。切流需单独授权。
- 目标已接写后不能直接解冻旧库回滚；新 timeline 的备份保护须单独验收。
- 旧机保留至少七天；历史备份与解密材料不随定时任务退役删除。
- **单窗闭环（硬）：** 上表终态块与布局在 cutover 同窗收口；**禁止**把 Stage0 布局/搬 Docker Root/全量改名排成迁机后第二次停机。  
  例外路径细则：[`stage0-layout-convergence.md`](../../../deploy/tencent/stage0-layout-convergence.md)。

## 布局：写进 plan/prepare，落在 cutover 同窗

验收标准（切流结束当日必须满足，不是后续 follow-up）：

| 项 | 要求 |
|---|---|
| 业务 `.env` | `/var/lib/tokenkey/.env`（Stage0 默认路径） |
| 网关容器 | `tokenkey` / `tokenkey-caddy`（或等价 Stage0 名） |
| PG / Redis | 可用 Stage0 名 **或** 网络别名 `postgres` / `redis`；探测脚本须双名兼容 |
| Docker Root | **默认不搬**：可保留演练/加密挂载下的 data-root；**不**改 daemon.json、**不**停 Docker 守护进程作为迁机必选项 |
| 发版 | 切流 verify 通过后即可 `deploy-stage0`（首次蓝绿按脚本从 legacy `tokenkey` 迁即可） |

做法：

1. **plan**：列出布局目标上表 + 上一节终态块；写明「无 post-cutover 布局/控制面返工窗」；Docker Root 若保留例外须写入 SSOT。  
2. **prepare**：目标机按上表起栈（或 rehearsed rename/alias 剧本）；不要故意以 `formal/.env` + `tk-cutover-*` 作为「切流后再说」的终态。  
3. **cutover**：promote/DNS 与布局对齐同一维护窗完成（细则同上；`apply-tencent-layout-window.sh` 仅显式 `APPLY=…`）。  
4. **verify**：确认布局表 + 终态块（resolve/`PROD_SSM_TARGET=auto`、备份 timer、飞书、契约）；**不以**「下周再补」关闭迁机。

完整字面 Stage0（物理改名 PG/Redis、搬 data-root）只作可选整洁，**不得**挡发版，也不得绑进迁机主路径。


报告实际完成阶段、恢复点/账务校验、耗时、资源清理、保护缺口与下一步；
不以数据库恢复或健康探针成功宣称整站迁机完成。
