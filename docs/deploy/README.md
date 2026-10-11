# Deployment docs

现网入口与 AWS 保留依赖的唯一说明：[Hetzner fleet](../../deploy/hetzner/README.md)。
日常发布用 `scripts/stage0/dispatch-prod-deploy.sh` / `dispatch-edge-deploy.sh`；
目标身份由共享 resolver 派生。`deploy/aws/stage0/` 仍含现网共享运行文件，目录名不表示 AWS 部署。

| File | Topic |
| --- | --- |
| [`blue-green-zero-downtime-backlog.md`](blue-green-zero-downtime-backlog.md) | Prod/Edge 蓝绿契约与入口 |
| [`tokenkey-edge-ip-history.md`](tokenkey-edge-ip-history.md) | AWS standby IP 与污染排除历史 |
| [`aws-us-openai-gateway-deployment.md`](aws-us-openai-gateway-deployment.md) | 历史 AWS 部署，仅用于旧机考古/恢复 |

QA 尚未正式完成去 AWS，范围与验收见 [审批基线](../approved/hcloud-bluegreen-qa-migration.md)。
