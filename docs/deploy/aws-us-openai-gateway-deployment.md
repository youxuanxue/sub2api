# TokenKey AWS 部署入口

Prod 使用 EC2/CloudFormation，Edge 使用 Lightsail。部署参数以模板和目标矩阵为准；
本页只保留首次 bootstrap 的说明，日常发布、回滚、备份和恢复使用各自的现行入口。

| 任务 | 唯一入口 |
| --- | --- |
| Prod 首次基础设施创建 | `deploy/aws/cloudformation/stage0-single-ec2.yaml`；下方 bootstrap |
| Edge 新建与配置 | [`deploy/aws/lightsail/`](../../deploy/aws/lightsail/) 与 `tokenkey-stage0-edge-lightsail-expansion` skill |
| 应用发布与回滚 | [`deploy/aws/README.md`](../../deploy/aws/README.md)；契约见 [`deploy-stage0-workflow.md`](../approved/deploy-stage0-workflow.md) |
| QA Worker / maintenance 独立发布 | [`prod-component-release.md`](../approved/prod-component-release.md) 与 [`design-split-deploy-qa-bundle.md`](../approved/design-split-deploy-qa-bundle.md) |
| 备份、整机与数据恢复 | [`RUNBOOK-disaster-recovery.md`](../../deploy/aws/RUNBOOK-disaster-recovery.md) |
| QA 归档、导出与生命周期 | [`ops/qa/README.md`](../../ops/qa/README.md) |
| 目标实例、域名与预算 | Prod stack outputs；Edge `deploy/aws/lightsail/edge-targets-lightsail.json` |

## Prod 首次 bootstrap

前置条件：AWS CLI 已配置，DNS 可控，fork 的 GHCR 镜像已发布，拉取凭据已经写入
SSM SecureString `/tokenkey/ghcr/pat`。OIDC、参数与凭据配置步骤见
[`deploy/aws/README.md`](../../deploy/aws/README.md)。不要使用上游镜像替代 TokenKey 发布镜像。

CloudFormation 参数是创建时配置；实际允许值与默认值直接读取模板。
创建基础设施前需要确认 AWS 资源和费用范围。

```bash
aws cloudformation deploy \
  --region us-east-1 \
  --stack-name tokenkey-prod-stage0 \
  --template-file deploy/aws/cloudformation/stage0-single-ec2.yaml \
  --capabilities CAPABILITY_IAM \
  --parameter-overrides \
    ApiDomain=api.tokenkey.dev \
    AcmeEmail=<运维邮箱> \
    GhcrOwner=<GitHub用户名> \
    GhcrPullUser=<GHCR拉取用户名>
```

创建后从 stack outputs 读取 Elastic IP 和 InstanceId，配置 DNS，通过 SSM 查看
`/var/log/tokenkey-bootstrap.log` 并验证 `/health`。首次管理账号和网关配置按
管理后台及接入指南完成。生产 `.env`、数据库密码、JWT/TOTP 密钥不入库。

CFN payload 由 `deploy/aws/stage0/build-cfn.sh` 从 canonical compose、Caddy 和
host scripts 生成。修改这些源文件后运行生成脚本及 `--check`，不手改内嵌 payload。

## 应用与 QA 生命周期边界

日常应用升级和前一版本回滚使用现行 release workflow，以及共享的
`ops/stage0/deploy_via_ssm_bluegreen.sh`。不要通过 CFN `ImageTag` 更新或单容器
`docker compose up` 另建发布路径；基础设施更新与应用切换是不同操作。

`tokenkey-qa-maintenance.sh` 是目标 QA lifecycle owner。
`tokenkey-qa-boundary.sh` 是 transition-only 工件；single-owner activation 前的行为、
激活 receipt 与回滚约束只在 [`ops/qa/README.md`](../../ops/qa/README.md) 维护。
旧 stale-cleanup 和 export-orphan 工件不再打包。应用回滚不等于 QA 控制面回滚。

## 架构演进

RDS、跨实例负载均衡和 Multi-AZ 属于独立的基础设施设计决策，应基于实际负载、
恢复目标与账单评估。历史阶段成本表、静态节点登记表和停机估计保留在 Git 历史，
不作为当前资源状态或升级触发契约。
