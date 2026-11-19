# Tokyo → 腾讯美西：切流清单（已关闭）

状态：**关闭**（2026-11-19）。Tokyo 已退役；现网为腾讯美西 prod + 北京 cn-prod。

**现网事实只读** [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。交接摘要：[`tencent-migration-handoff.md`](tencent-migration-handoff.md)。切流回执（历史）：`docs/ops/reports/tencent-cutover-20261022.json`。

本文件不再维护开放项或过程史。后续决策见 [`docs/decisions/decided.md`](../decisions/decided.md) / [`declined.md`](../decisions/declined.md)。

已明确不做（详见 declined）：整机/卷级快照常备策略；AWS 临时机隔离恢复演练；QA→S3 上传；data-layer archive canary；操作员 RSA 二次加密 host-config。

Hybrid `mi-*` 加入/替换时的 CI `SendCommand` 纪律在舰队入口「AWS 残留」SSM Hybrid 行，不在本文件。
