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

## 判断与不变量

- 先核实备份、连续 WAL 和最新/历史恢复证据；镜像、架构、PG 主版本和恢复参数兼容。
- 演练只写新盘，不挂生产卷，不运行真实业务写者/外部回调，不改 DNS。
- 现有恢复工具是自动清理的验收工具，不能拿来安装持久目标库。
- 正式切换：standby 追 WAL → 源写入隔离 → 最终 WAL/LSN 入仓与重放确认 → 目标 promote → DNS。
  默认只读 ≠ 完整写入隔离；严禁双写。切流需单独授权。
- 目标已接写后不能直接解冻旧库回滚；新 timeline 的备份保护须单独验收。
- 旧机保留至少七天；历史备份与解密材料不随定时任务退役删除。
- **单窗闭环（硬）：** `plan` 起就必须规划「切流窗结束即可默认 `deploy-stage0`」。  
  **禁止**把 Stage0 布局收敛、搬 Docker Root、全量容器改名排成迁机完成后的第二次停机。  
  2026-10-22 教训：主机已切流后另开布局窗 = 多余停机与回归；见 [#2779](https://github.com/youxuanxue/sub2api/pull/2779) 例外路径。

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

1. **plan**：列出布局目标上表；写明「无 post-cutover 布局停机窗」；Docker Root 若保留例外须写入 SSOT。  
2. **prepare**：目标机按上表起栈（或 rehearsed rename/alias 剧本）；不要故意以 `formal/.env` + `tk-cutover-*` 作为「切流后再说」的终态。  
3. **cutover**：promote/DNS 与布局对齐同一维护窗完成（可用 `apply-tencent-layout-window.sh` 一类短窗脚本；细则 [`stage0-layout-convergence.md`](../../../deploy/tencent/stage0-layout-convergence.md)）。  
4. **verify**：确认上表 + resolve/`PROD_SSM_TARGET=auto` + 备份 timer；**不以**「布局留待下周」关闭迁机。

完整字面 Stage0（物理改名 PG/Redis、搬 data-root）只作可选整洁，**不得**挡发版，也不得绑进迁机主路径。

## 切流窗还须收口（2026-10-22 曾漏）

1. **控制面**：`resolve --target auto` → 新主；GitHub `PROD_SSM_TARGET=auto`；旧身份失败关闭；`ssm_execution` 等 glue 显式分支新平台。  
2. **备份身份**：`RuntimeDirectoryPreserve=yes`；新 timeline 独立前缀；identity/backup/archive-check timer；飞书告警接到新机。  
3. **DNS/TTL**：切前降 TTL、切后恢复；全部正式名（含 `api-cn`）用公共 resolver + `curl --resolve`。  
4. **出口**：主机切流 ≠ 出口切流；另测 `usage_logs` 再迁 `proxy_id`，保留回滚清单。  
5. **verify**：fence/usage、上游 vs 平台错误、`archive_check`、代理分布——不只 `/health`。

历史踩坑见 [playbook](references/playbook.md)。

报告实际完成阶段、恢复点/账务校验、耗时、资源清理、保护缺口与下一步；
不以数据库恢复或健康探针成功宣称整站迁机完成。
