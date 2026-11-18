# 腾讯运维控制面门禁

SSOT：[`docs/approved/design-tencent-ops-control-plane.md`](../../docs/approved/design-tencent-ops-control-plane.md)

两条纪律：

1. 管腾讯机日常默认 = TAT（既有 `remote_exec` / `tencent-ops`）
2. ops 密钥 = `tencent-ops` secret、主机 root-only、或腾讯 COS；禁止 SecureString 新写

门牙：`scripts/checks/tencent-ops-control-plane-gate.py` +
`legacy-aws-control-plane-allowlist.json`。

- 新文件出现 `ssm send-command`（OIDC→SSM 管机）或不在清单内的 SecureString 写入 → 合不进来
- 迁完一条：去掉违例模式，并**同 PR 从 allowlist 删除**该路径
- 禁止靠扩 allowlist 开新旁路；例外须另批并改设计
