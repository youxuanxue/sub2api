---
name: tokenkey-host-replacement
description: >-
  TokenKey prod 换机/整机迁移与跨云恢复演练：pgBackRest 全量/增量/WAL、
  隔离验收、standby 追赶、单写切流与旧机保留。Not image-only deploy or IP rotation.
---

# TokenKey：新机迁移与隔离恢复

现网：[舰队入口](../../../deploy/hetzner/README.md)。
流程与能力边界：[pgBackRest 迁机工作流](../../../ops/stage0/pgbackrest-host-migration.md)。
备份/恢复命令：[physical-backup.md](../../../ops/stage0/physical-backup.md)。

输入 `target=prod`、目标云/地域/架构、`phase=plan|rehearse|prepare|cutover|verify`。
默认先 plan；已授权阶段直接推进。腾讯云演练不等于授权创建付费资源或正式切流。
仅镜像部署用 `tokenkey-stage0-release-rollout`；cn-prod 未验收物理备份，不套用 prod 的退役决策。

## 判断与不变量

- 先核实备份、连续 WAL 和最新/历史恢复证据；镜像、架构、PG 主版本和恢复参数兼容。
- 演练只写新盘，不挂生产卷，不运行真实业务写者/外部回调，不改 DNS。
- 现有恢复工具是自动清理的验收工具，不能拿来安装持久目标库；跨云身份和 standby 适配未完成时如实停在准备阶段。
- 正式切换使用 standby 追 WAL，源写入隔离 → 最终 WAL/LSN 入仓和重放确认 → 目标 promote。
  默认只读不构成完整写入隔离；严禁双写。切流需单独授权。
- 目标已接写后不能直接解冻旧库回滚；新 timeline 的备份保护须单独验收。
- 旧机保留至少七天；历史备份与解密材料不随定时任务退役删除。

报告实际完成阶段、恢复点/账务校验、耗时、资源清理、保护缺口与下一步；
不以数据库恢复或健康探针成功宣称整站迁机完成。历史切流踩坑按需读 [playbook](references/playbook.md)。
