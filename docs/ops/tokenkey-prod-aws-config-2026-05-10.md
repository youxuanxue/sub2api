# TokenKey 生产环境 AWS 配置快照

> 查询日期：2026-05-10  
> 环境：Production / Stage0  
> 区域：AWS `us-east-1`  
> 来源：CloudFormation、EC2、SSM、EBS、CloudWatch 只读查询结果  
> 说明：本文包含基础设施标识符，适合与合作伙伴定向同步，不建议公开传播。

## 1. 总览

TokenKey 生产环境当前运行在 AWS Stage0 单机架构上：一台 ARM64 Graviton EC2 实例承载 Caddy、TokenKey 应用、PostgreSQL 与 Redis，业务数据放在独立 EBS Data Volume 中。

| 项 | 当前值 |
|---|---|
| CloudFormation Stack | `tokenkey-prod-stage0` |
| Stack 状态 | `UPDATE_COMPLETE` |
| 服务域名 | `https://api.tokenkey.dev` |
| 公网 Elastic IP | `34.194.234.88` |
| EC2 Instance ID | `i-0e4a90677f0450277` |
| 实例状态 | `running` |
| AWS 区域 | `us-east-1` |
| 可用区 | `us-east-1a` |
| VPC | `vpc-0337cf2929fd56c45` |
| Subnet | `subnet-078d680406b580ed4` |
| 内网 IP | `10.0.1.97` |

## 2. 计算资源

| 项 | 当前值 |
|---|---|
| 实例类型 | `t4g.small` |
| CPU | 2 vCPU |
| Core / Thread | 2 cores，1 thread/core |
| 内存 | 2048 MiB / 2 GiB |
| 架构 | `arm64` |
| 处理器平台 | AWS Graviton |
| 持续主频 | 2.5 GHz |
| 网络性能 | Up to 5 Gigabit |
| CloudWatch Detailed Monitoring | `disabled` |

## 3. 操作系统与 AMI

| 项 | 当前值 |
|---|---|
| 操作系统 | Amazon Linux 2023 |
| Platform Type | Linux |
| Platform Details | Linux/UNIX |
| AMI ID | `ami-0d6fc8f787cd9a417` |
| AMI 名称 | `al2023-ami-2023.11.20260427.1-kernel-6.1-arm64` |
| AMI 描述 | Amazon Linux 2023 AMI 2023.11.20260427.1 arm64 HVM kernel-6.1 |
| AMI 创建时间 | `2026-04-29T19:42:11Z` |
| Root Device Type | EBS |
| Virtualization Type | HVM |
| SSM Agent | `3.3.4108.0` |
| SSM 状态 | Online |
| Hostname | `ip-10-0-1-97.ec2.internal` |

## 4. 存储配置

当前实例挂载两块 gp3 EBS 卷：root volume 和独立 data volume。两块卷均加密，且均未设置随实例终止自动删除。

| 用途 | Device | Volume ID | 大小 | 类型 | IOPS | Throughput | 加密 | DeleteOnTermination |
|---|---|---|---:|---|---:|---:|---|---|
| Root Volume | `/dev/xvda` | `vol-0c06d9f4fd21ac0ea` | 30 GiB | gp3 | 3000 | 125 MB/s | true | false |
| Data Volume | `/dev/sdf` | `vol-020ce8eda4cf1e5ea` | 30 GiB | gp3 | 3000 | 125 MB/s | true | false |

Data Volume 用于持久化 PostgreSQL、Redis、Caddy 状态与应用数据。CloudFormation 对该卷使用保留策略，实例替换时数据卷可重新挂载到新实例。

## 5. 镜像与部署参数

| 项 | 当前值 |
|---|---|
| GHCR Owner | `youxuanxue` |
| GHCR Image Name | `sub2api` |
| CloudFormation `ImageTag` | `1.7.11` |
| Resolved Image | `ghcr.io/youxuanxue/sub2api:1.7.11` |
| GHCR PAT SSM 参数名 | `/tokenkey/ghcr/pat` |
| Timezone | `UTC` |
| Snapshot Schedule | `daily` |
| QA stale retention | 1.5 days |

说明：上述镜像信息来自 CloudFormation 参数与输出。生产升级通常通过 SSM 原地更新容器镜像，若需要核对主机内当前实际运行容器 tag，可再通过 SSM 进入实例查询 Docker 运行态。

## 6. 网络与安全组

| 项 | 当前值 |
|---|---|
| Security Group ID | `sg-0e8a46cde302dc078` |
| Security Group Name | `tokenkey-prod-stage0-AppSecurityGroup-1Jjmsb4k8W2x` |
| IAM Instance Profile | `tokenkey-prod-stage0-InstanceProfile-NjOBxuhWm5Qj` |
| IMDSv2 | Required |
| Metadata Endpoint | Enabled |

### 入站规则

| 协议 | 端口 | 来源 | 用途 |
|---|---:|---|---|
| TCP | 80 | `0.0.0.0/0` | HTTP / Let's Encrypt HTTP-01 / redirect |
| TCP | 443 | `0.0.0.0/0` | HTTPS |
| TCP | 22 | `0.0.0.0/0` | SSH |

### 出站规则

| 协议 | 端口 | 目标 |
|---|---|---|
| All | All | `0.0.0.0/0` |

### 安全备注

当前 SSH `22/tcp` 对全网开放。运维文档建议优先使用 SSM Session Manager；如果没有必须保留公网 SSH，建议将 `AdminCidr` 收紧到固定办公出口 IP，或设为 `127.0.0.1/32` 以禁用公网 SSH。

## 7. 备份与告警

| 项 | 当前值 |
|---|---|
| DLM Snapshot Policy | `policy-061a66f72b46fe7f0` |
| Snapshot Schedule | daily |
| Data Volume 告警 | `tokenkey-prod-data-volume-used` |
| 告警 Namespace | `tokenkey/EC2` |
| 告警 Metric | `DataVolumeUsedPercent` |
| 告警阈值 | 大于 90% |
| 评估周期 | 300 秒 × 2 |
| 当前告警状态 | `OK` |
| Alarm Actions | 未配置 |

## 8. 当前容量摘要

| 资源 | 当前规格 |
|---|---|
| CPU | 2 vCPU |
| 内存 | 2 GiB |
| Root Disk | 30 GiB gp3 |
| Data Disk | 30 GiB gp3 |
| 架构 | ARM64 / Graviton |
| OS | Amazon Linux 2023 |
| 部署形态 | 单 EC2 Stage0，全栈 Docker Compose |

## 9. 建议事项

1. 收紧 SSH 入站规则，避免 `22/tcp` 长期暴露在 `0.0.0.0/0`。
2. 如需对外承诺运行镜像版本，建议补充一次主机内 Docker 运行态核验。
3. 如果合作伙伴关注可用性边界，应明确当前为 Stage0 单机架构，不是多 AZ 高可用架构。
