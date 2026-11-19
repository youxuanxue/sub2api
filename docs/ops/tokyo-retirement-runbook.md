# Tokyo / 日本 Lightsail 退役（已完成）

状态：**已完成**（2026-10-23）。实例、栈、VPC、卷、EIP、快照均已删除。

**唯一留存**：S3 历史链 `prod/postgres`（timeline 1，仅数据库，只读）。禁止恢复成写库、禁止双写。

现网坐标：[`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。执行细节在 Git 历史；勿再按本路径开快照或重建 Tokyo。
