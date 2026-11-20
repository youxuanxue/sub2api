# pgBackRest 新机迁移

现网身份：[舰队入口](../../deploy/hetzner/README.md)。
备份/验收命令：[physical-backup.md](physical-backup.md)。
整机替换流程：skill [`tokenkey-host-replacement`](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)
与 [playbook](../../.cursor/skills/tokenkey-host-replacement/references/playbook.md)。

**规划纪律：** 新机从 `plan` 起按正式迁机终态一次写清（布局/控制面/备份告警/DNS/出口/SSOT），
与 cutover **同窗**收口；禁止切后再开多轮返工停机。

## 边界

- 本文替代 prod 全库 dump + 按表尾差迁机路径；cn-prod 备份验收见 [domestic README](../../deploy/domestic/README.md)。
- 物理恢复验收 ≠ 整站切流验收。隔离恢复/standby 演练细节以 skill playbook 为准。
- Tokyo / JP Lightsail / LUKS 迁机执行史已删除（2026-11 收尾）；勿按已删步骤重建 LUKS 或 AWS 临时恢复机默认路径。
- 旧 `pgdump_restore_canary.py`、尾差/冻结脚本仅历史回查，不是 prod 迁机默认入口。

## 执行入口

按 `tokenkey-host-replacement` playbook 推进：身份矩阵 → 新机布局 → pgBackRest 追赶 →
窗口切流 → 备份/告警/SSOT 同窗收口。具体命令与确认口令以 playbook / physical-backup 为准。
