# 腾讯 COS 成本与资源清单（只读盘点）

盘点时间：2026-10-31 UTC。本文只记录现网 AWS 备份对象的只读统计、估算公式和待填写资源清单；不创建 COS bucket、不修改 pgBackRest、不启动 timer。

## 现网对象规模

通过 AWS `s3api list-objects-v2` 分别读取现有 pgBackRest 前缀，未发现分页截断：

| 环境 | 当前对象数 | 当前大小 | 近 7 个完整日新增 |
|---|---:|---:|---:|
| prod | 12,995 | 38.41 GiB | 28.17 GiB |
| cn-prod | 8,409 | 3.88 GiB | 3.08 GiB |
| 合计 | 21,404 | 42.29 GiB | 31.25 GiB |

当前 prod 前缀为 AWS `us-east-1` 的 `candidates/tencent-usw-20261022/postgres`，cn-prod 前缀为 AWS 东京 `ap-northeast-1` 的 `cn-prod/postgres`。对象按 `backup` 和 `archive` 两类统计；不能把“最近一天新增量”直接当作最终保留容量，完整链和 WAL 依赖必须一起计算。

## 成本估算

腾讯 COS 官方价格页当前展示的起价（2026-10-31 抓取）为：标准存储单地域约 **0.099 元/GB/月**，北京等多 AZ 标准存储约 **0.15 元/GB/月**。这是公开起价，不是账号最终报价；请求、外网流量、取回、KMS 和跨地域复制未计入。

按 `GiB × 1.073741824 = GB` 粗略换算：

| 场景 | prod 美西（0.099） | cn-prod 北京（0.15） | 合计存储费/月 |
|---|---:|---:|---:|
| 当前已盘点 42.29 GiB | 约 4.08 元 | 约 0.63 元 | **约 4.71 元** |
| COS 7 天窗口 31.25 GiB | 约 2.99 元 | 约 0.50 元 | **约 3.49 元** |
| 若连续保留 30 天新增量 133.93 GiB | 约 12.84 元 | 约 2.13 元 | **约 14.97 元** |

数据库 COS 主链按批准设计仍使用 pgBackRest 的 7 天链清理；上表 30 天只用于 node1 兜底容量和成本敏感性参考，不能据此改变 COS retention。COS lifecycle 只处理未完成 multipart，不能与 pgBackRest expire 同时管理有效备份对象。

正式预算采用腾讯控制台或商务报价中的实际地域、存储类型和阶梯价格，并另列：首次全量上传流量、持续 WAL/增量写入、node1 每日下载流量、恢复时取回流量、KMS 请求和 API 请求。没有这些报价前，不把“约 5 元/月”当成最终账单承诺。

## 待填资源清单

| 项目 | prod → 美西 | cn-prod → 北京 | 验收要求 |
|---|---|---|---|
| bucket | `待创建/填写` | `待创建/填写` | 私有、独立 bucket |
| region / endpoint | `待填写` | `待填写` | 与腾讯控制台实际地域一致 |
| pgBackRest prefix | `prod/postgres` | `cn-prod/postgres` | 不复用 AWS prefix，不混 timeline |
| 备份账号 | `待填写` | `待填写` | 仅 list/get/put/abort 等最小权限 |
| 恢复账号 | `待填写` | `待填写` | 只读 |
| KMS / 加密策略 | `待填写` | `待填写` | 密钥 owner、轮换和恢复权限明确 |
| node1 兜底目录 | `db-backup/prod/` | `db-backup/cn-prod/` | 独立 namespace、manifest、receipt |
| 告警接收方 | `待填写` | `待填写` | COS 写入失败、WAL 延迟、容量不足 |

SecretId、SecretKey、KMS 密钥材料和签名 URL 不进入 Git、`.env`、日志或回执。资源清单完成前不得执行真实 PUT。

## 下一步 dry-run / 演练顺序

1. 用合成 PostgreSQL 和 S3 兼容测试端点验证 pgBackRest 的 COS endpoint、path-style、签名、bundle/block 和 WAL 行为。
2. 用只读 COS 凭据做对象清单和 `rclone size`，生成预计下载字节数；node1 工具先跑容量 dry-run，再做小快照 rehearsal。
3. 在隔离环境验证 `repo1` 与 `repo2` 的 `--repo=all backup/check/verify`，确认一个仓库失败时整体失败且 AWS 链不受影响。
4. 形成真实资源清单和费用上限后，另立生产变更单；先双写观察，再做 COS 恢复验收。

以上步骤均不包含 AWS 删除、COS lifecycle 删除、线上重启或 QA timer 启用。node1 扩盘完成后，才接入 QA 365 天归档和 COS→node1 每日兜底的真实定时任务。
