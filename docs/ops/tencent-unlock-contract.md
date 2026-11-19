# LUKS 解锁契约（已退役）

状态：**2026-11-19 X1–X4 完成；LUKS 不再是现网数据面。**

现网应用机明文根盘（`volume_platform=tencent-root`）：`/var/lib/tokenkey`、Docker Root `/var/lib/docker`。载体、COS 解锁钥、操作员 vault、unlock unit 已销毁。

**禁止**再执行解锁演练、自动解锁验收或重建 LUKS 回退路径。舰队 SSOT：[`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)；授权见 [`docs/decisions/decided.md`](../decisions/decided.md)。
