# 运维工件

**不是**日常运营入口。Admin UI 操作手册 → [`../operator/README.md`](../operator/README.md)。

本目录只放：CI/脚本绑定的基线、排障事实存档、基础设施快照、工程 backlog。

| 文件 | 用途 |
|------|------|
| [cc-fingerprint-changelog.md](./cc-fingerprint-changelog.md) | Claude Code 指纹对齐 changelog（skill 追加） |
| [antigravity-fingerprint-changelog.md](./antigravity-fingerprint-changelog.md) | Antigravity 指纹对齐 changelog（skill 追加） |
| [kiro-fingerprint-changelog.md](./kiro-fingerprint-changelog.md) | Kiro CLI 指纹对齐 changelog（skill 追加） |

命名约定：带日期后缀的报告由对应 skill / 脚本刷新；不要把一次性排障笔记堆回本目录。

## Prod-only operator utilities

以下入口只服务当前 prod 主机或一次性迁移/审计：

`export_adspower_session.py`, `apply-account-contact-email.sh`, `probe-account-emails.sh`, `probe-alert-config.sh`, `probe-data-layer-retention-inventory.sh`, `probe-fleet-traffic-window.sh`, `probe-ssot-recent-success.sh`, `probe-traffic-logs.sh`, `probe-user-traffic-window.sh`, `profile-traffic.py`, `fetch-error-clusters.sh`, `fetch-logs.sh`, `prod_qa_maintenance.py`, `check-cfn-live-drift.sh`, `retire-aws-standby-timers-remote.sh`, `retire_dormant_media_storage_remote.sh`, `sync-instance-cpu-alarm.sh`, `sync-instance-root-disk-alarm.sh`, `sync-prod-pg-tuning-via-ssm.sh`。
