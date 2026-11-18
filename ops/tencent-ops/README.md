# 腾讯运维控制面门禁

SSOT：[`docs/approved/design-tencent-ops-control-plane.md`](../../docs/approved/design-tencent-ops-control-plane.md)
收尾处置：[`docs/approved/design-tencent-ops-closeout.md`](../../docs/approved/design-tencent-ops-closeout.md)

两条纪律：

1. 管腾讯机日常默认 = TAT（`deploy.domestic.remote_exec`、`run-probe --via tat`、`ssm_execution` 默认 TAT）
2. ops 密钥 = `tencent-ops` secret、主机 root-only、或腾讯 COS；禁止 SecureString 新写

门牙：`scripts/checks/tencent-ops-control-plane-gate.py` +
`legacy-aws-control-plane-allowlist.json`。

- 新文件出现 `ssm send-command`（OIDC→SSM 管机）或不在清单内的 SecureString 写入 → 合不进来
- OIDC workflow 若调用 `run-probe.sh` / `ssm_execution` / `manage-overlay-runtime.py` 也算管机入口
- 迁完一条：去掉违例模式，并**同 PR 从 allowlist 删除**该路径
- allowlist 相对 `origin/main` **只许缩不许增**（门牙机械拒绝扩表；首次落地除外）
