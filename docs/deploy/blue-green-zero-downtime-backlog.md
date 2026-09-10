# Stage0 蓝绿发布参考

Prod EC2 与 Lightsail Edge 的常规升级、回滚共用
`ops/stage0/deploy_via_ssm_bluegreen.sh`。现行行为以
[`deploy-stage0-workflow.md`](../approved/deploy-stage0-workflow.md) 为唯一审批基线，
Edge 接入决策见
[`edge-bluegreen-release-safety.md`](../approved/edge-bluegreen-release-safety.md)。

## 操作入口

- 发布、升级与前一版本回滚：[`deploy/aws/README.md`](../../deploy/aws/README.md)。
- 整机、系统或数据卷恢复：[`RUNBOOK-disaster-recovery.md`](../../deploy/aws/RUNBOOK-disaster-recovery.md)。
- 发布窗口可见性测量：`ops/stage0/measure_deploy_blackout.sh`。
- 本地发布验证：`tokenkey-stage0-local-deploy` skill。

旧版本文描述的 Edge 单容器升级、EC2 Edge compose 拆分和按固定等待时间切色，
已被共用蓝绿发布 owner 及其容量、就绪、切色与恢复检查取代。
历史方案和当时的停机测量保留在 Git 历史中，不再作为当前操作步骤。

跨实例或外部负载均衡属于另行设计的基础设施变更。当前没有在本文维护第二套
发布状态机或未经验证的零停机承诺；发布验收读取实际 workflow 和 smoke 结果。
