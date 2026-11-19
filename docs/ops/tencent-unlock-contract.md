# 腾讯美西 · LUKS 解锁与挂载契约（已退役）

状态：**2026-11-19 X1–X4 完成；LUKS 不再是现网数据面。**  
现网 `app-old` / `app-cand` 均为明文根盘（`volume_platform=tencent-root`），业务目录 `/var/lib/tokenkey`、Docker Root `/var/lib/docker`。  
舰队 SSOT：[`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)；矩阵：[`deploy/tencent/candidate-usw-20261022.json`](../../deploy/tencent/candidate-usw-20261022.json)。

## 退役事实（2026-11-19）

| 项 | 结果 |
|---|---|
| 载体 | `/var/lib/tokenkey-validation/recovery-rebuild-20261108.luks` 已 `luksErase` 并删除（约 64 GiB 释放） |
| mapper / loop | 销毁前已确认 inactive / 无 loop |
| COS 自动解锁钥 | `tokenkey-prod-backup-usw-1320330536` `custody/luks/20261108/{recovery-luks-20261108.key.enc,luks-wrap-20261108.key}` 已删；仅保留 `luks-rebuild-receipt.json` |
| 操作员 vault | 本机 `recovery-luks-20261108.key.enc` / `luks-wrap-20261108.key` 已 shred |
| unlock unit/脚本 | `tokenkey-luks-unlock.service` 与 `/usr/local/sbin/tokenkey-luks-unlock` 已删 |
| 主机回执 | `/var/lib/tokenkey-validation/luks-x1-x4-destroy-20261119T092329Z.json` |
| 用户流量 | 全程 `tokenkey` healthy，`https://api.tokenkey.dev/health=200` |

授权见 [`docs/decisions/decided.md`](../decisions/decided.md)（缩短 §3.5 观察窗的提前收尾）。

## 历史（切流期，仅供考古）

以下描述 **2026-10-22→2026-11-18** 切流与阶段 D 之前的契约，**不可再执行**：

- 异机密封密文 + wrap key → tmpfs → `cryptsetup open` → 双挂 `/var/lib/tokenkey` 与 `/srv/tokenkey-rehearsal`
- 禁止把密钥写回目标盘做「自动开机」；禁止拿现网整机重启做解锁验收
- 只读探针曾用 `ops/observability/probe-tencent-unlock-health.sh`（现网明文布局下 mapper 缺失告警属预期，勿当作故障回滚依据）

关联历史：[`tencent-cutover-checklist.md`](tencent-cutover-checklist.md)、[`stage0-layout-convergence.md`](../../deploy/tencent/stage0-layout-convergence.md)、[`design-prod-entry-multi-app.md`](../approved/design-prod-entry-multi-app.md)。
