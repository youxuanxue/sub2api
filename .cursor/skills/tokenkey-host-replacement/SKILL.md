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

输入 `target=prod`、目标云/地域/架构、`phase=plan|rehearse|prepare|cutover|verify|converge`。
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

## 切流后必做（verify / converge）— 2026-10-22 教训

这些步骤在 Tokyo→腾讯美西切流中曾漏做或事后才发现，**必须写进阶段门禁**，不能靠自觉：

1. **控制面目标**  
   - `resolve_prod_ssm_target.py --target auto` 必须指向新主；矩阵 `deployable=true`。  
   - 立刻把 GitHub 仓库变量 `PROD_SSM_TARGET` 设为 `auto`（钉死旧身份会覆盖 workflow 默认值）。  
   - 旧身份失败关闭：`aws`/`hetzner` 退役拒绝；冻结源须 `ALLOW_FROZEN_TOKYO_TARGET=1`。  
   - 共享 glue（如 `ssm_execution.py`）必须显式分支到新平台，禁止 `else → 旧 Hybrid`。

2. **布局收敛门禁（阻塞默认发版）**  
   - 切流窗口可用临时路径（如 `formal/.env`、`tk-cutover-prod-*`、Docker root 在演练挂载下）。  
   - **未收敛到 Stage0 约定（`/var/lib/tokenkey/.env` + 标准 compose 名）前，禁止默认 `deploy-stage0`。**  
   - 舰队 SSOT / 切流回执 / unlock 契约必须写清实况路径，禁止假装已是 Stage0 布局。

3. **备份身份与 timer**  
   - Hybrid 备份身份：`RuntimeDirectoryPreserve=yes`；oneshot 结束后 tmpfs 不可被 systemd 清掉。  
   - 新 timeline 用独立前缀（勿混写旧 `prod/postgres`）；装上 identity/backup/archive-check timer。  
   - 外部飞书备份告警须接到新机（本机 timer ≠ 外部告警已通）。

4. **DNS / TTL / 多域名**  
   - 切流前降低 TTL，切流后恢复（Porkbun 等下限可能 >60）。  
   - 所有正式名（含 `api-cn`）一并验证；本机缓存不能当结论。

5. **出口代理**  
   - 主机切流 ≠ 出口切流。跨云后测真实 `usage_logs` 延迟，再决定 JP→同城代理迁移。  
   - 账号 `proxy_id` 批量更新要有回滚清单；迁后只读确认 `jp_remaining=0`。

6. **verify 不只看 /health**  
   - fence/usage、错误率（区分上游 503 噪声）、备份 `archive_check`、代理绑定分布、resolve 身份。

历史踩坑细节见 [playbook](references/playbook.md)。

报告实际完成阶段、恢复点/账务校验、耗时、资源清理、保护缺口与下一步；
不以数据库恢复或健康探针成功宣称整站迁机完成。
