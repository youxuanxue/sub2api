# Stage0 数据层迁移 SOP：prod 账本出机到 RDS

> 状态：PR1（平台改动，惰性）落地后按本 SOP 执行。
> 决策与实测依据见 PR 描述；架构原则：**冗余跟着数据的不可再生性走**——
> PG 里是账本（出机 + PITR），Redis 里是草稿纸（原地保留，全部可重建）。

## 0. 拓扑与不变量

```
迁移前（单机四容器）                迁移后
┌──────────────────────┐          ┌──────────────────────┐
│ EC2: Caddy + app     │          │ EC2: Caddy + app     │
│      + PG + Redis    │   ───→   │      + Redis（保留） │
└──────────────────────┘          └──────────┬───────────┘
                                             │ VPC 内网 5432, sslmode=require
                                  ┌──────────┴───────────┐
                                  │ RDS PostgreSQL 单节点 │ tokenkey-data-stage0 栈
                                  │ PITR 7 天（RPO ~5min）│ MultiAZ=true 在线可升
                                  └──────────────────────┘
```

**三条不变量（违反任何一条 = 停下来）：**

1. **运行时数据层配置的唯一真相是 SSM SecureString `/tokenkey/prod/stage0/data-layer-env`**。
   CFN 参数不携带任何数据层 endpoint/模式（防 ImageTag 退版同构事故：reboot/replace
   后按陈旧 CFN 参数静默回退本机模式 → 停用的旧本地 PG 带陈旧数据复活 = split-brain）。
2. **edge 一律不动**：所有 edge 保持本机 PG/Redis；存量 edge 的 compose 在下次
   reprovision 前保持旧版（实测确认 compose/.env 只在实例首启由 bootstrap 写入）。
3. **本机 postgres 数据卷在切换后保留 ≥14 天**（只 stop 容器不删卷），回滚是一条命令。

**Redis 出机触发器（写死，到点执行不再讨论）：**
以下任一发生时，Redis 必须迁 ElastiCache（机制已就绪：`REDIS_HOST`/`REDIS_ENABLE_TLS`
环境变量 + `localredis` profile + overlay，届时只是 overlay 多几行）：
- 上 app 第二副本之前（多副本共享 Redis 是硬前提）；
- 第一次发生 Redis 引发的线上事故。

**保留期 backlog（100× 前必须落地，本迁移不做）：**
实测账本增速 ~1.3KB/请求 → 100× 流量 ≈ 4.5GB/天 ≈ 1.6TB/年。
usage_logs 按月分区 + 90 天热存 + S3 归档；ops_system_logs / ops_error_logs
保留期硬化。**多大的 RDS 都喂不饱没有保留期纪律的明细账本**——RDS 存储
autoscaling 上限（200GB）是保险丝，不是方案。

## 阶段 A：平台就位（PR 合并后，线上零变化）

1. PR1 合并。验证惰性：发版任意一个 deployable edge + smoke
   （`tokenkey-stage0-release-rollout` skill）——edge 本机库照起、行为逐字节一致。
2. **wrapper fan-out**（幂等，不触碰服务）：对 prod + 每个 deployable edge 执行
   ```bash
   # prod
   AWS_REGION=us-east-1 ops/stage0/install_data_wrappers_via_ssm.sh <prod-instance-id>
   # Lightsail edge（逐个；mi-id 与 EDGE_ID 见 edge-targets-lightsail.json + SSM 控制台）
   AWS_REGION=<edge-region> EDGE_ID=<edge> ops/stage0/install_data_wrappers_via_ssm.sh <mi-id>
   ```
   脚本自带验证（`tokenkey-psql -c 'select 1'` + `tokenkey-redis-cli ping`）。
3. PR2 合并（ops 脚本翻转到 wrapper）。验证：跑一次
   `manage-anthropic-config.py snapshot` 全舰队通。

## 阶段 B：开通 + 预检（切换前 ~1 周）⛔×2

> 全程管理员本地凭证（同 resize SOP 理由——OIDC CI role 无 CFN/RDS 权限）。

1. ⛔ **app 栈更新**（加私有子网 + Exports，Instance 必须零变更）：
   ```bash
   aws cloudformation create-change-set --region us-east-1 \
     --stack-name tokenkey-prod-stage0 \
     --template-body file://deploy/aws/cloudformation/stage0-single-ec2.yaml \
     --capabilities CAPABILITY_IAM \
     --use-previous-template-defaults 2>/dev/null || true  # 参数沿用现值，ImageTag 严禁带新值
   aws cloudformation describe-change-set ...   # 人工确认：Instance 不出现，或 Action!=Modify/Replace；DataVolume 不出现
   aws cloudformation execute-change-set ...
   ```
   **铁律**：参数全部 `UsePreviousValue`（尤其 ImageTag——6 月 5 日退版事故）。
2. 生成并写入 RDS 主密码（值仅存 SSM，不落终端历史）：
   ```bash
   aws ssm put-parameter --region us-east-1 --type SecureString \
     --name /tokenkey/prod/stage0/rds-master-password \
     --value "$(openssl rand -hex 24)"
   ```
3. 确认引擎版本（18 优先，否则 17，并据此定 `TOKENKEY_PG_CLIENT_IMAGE`）：
   ```bash
   aws rds describe-db-engine-versions --engine postgres \
     --query 'DBEngineVersions[*].EngineVersion' --output text | tr '\t' '\n' | sort -V | tail -5
   ```
4. ⛔ **部署数据栈**：
   ```bash
   aws cloudformation deploy --region us-east-1 \
     --stack-name tokenkey-data-stage0 \
     --template-file deploy/aws/cloudformation/stage0-data.yaml \
     --parameter-overrides PgEngineVersion=<step3 结果> AlarmSnsTopicArn=<现有飞书 SNS>
   aws cloudformation describe-stacks --stack-name tokenkey-data-stage0 \
     --query 'Stacks[0].Outputs'   # 记下 PgEndpointAddress
   ```
5. **清理运维日志再演练**（实测：6.2GB 里 4.6GB 是 ops 日志；DELETE 即可，
   dump 不带死元组，无需 VACUUM FULL）：
   ```bash
   # 经 SSM 在 prod 上（按保留期裁剪，阈值按当时情况定，先 SELECT count 预览）
   sudo tokenkey-psql -c "DELETE FROM ops_system_logs WHERE created_at < now() - interval '14 days'"
   sudo tokenkey-psql -c "DELETE FROM ops_error_logs  WHERE created_at < now() - interval '14 days'"
   ```
6. **预检 + 演练（不切流）**，经 SSM 在 prod 上：
   ```bash
   # 连通 + TLS（容器网络出口直连 RDS）
   sudo docker run --rm --network "$(sudo docker network ls --format '{{.Name}}' | grep -m1 'tokenkey-network$')" \
     -e PGPASSWORD=<rds-pwd> postgres:<major>-alpine \
     psql "host=<rds-endpoint> sslmode=require user=tokenkey dbname=tokenkey" -c 'select version()'
   # 扩展（best-effort，rds master user 有权限）
   ... -c 'CREATE EXTENSION IF NOT EXISTS pg_trgm'
   # 完整 dump→restore 演练 + 行数对账 + 计时（结果决定 C 阶段窗口时长与公告）
   time (sudo tokenkey-pg_dump --format=plain --no-owner | \
     sudo docker run --rm -i --network <同上> -e PGPASSWORD=<rds-pwd> postgres:<major>-alpine \
       psql "host=<rds-endpoint> sslmode=require user=tokenkey dbname=tokenkey" -v ON_ERROR_STOP=1 -q)
   # 对账后清掉演练数据（重建库），保持 C 阶段 restore 进空库
   ```
7. （推荐）**全流程演练**：临时 EC2 + 小号数据栈完整跑一遍
   `cutover_data_layer_via_ssm.sh apply` → 验证 → `rollback` → reboot 一致性，
   再碰 prod。

## 阶段 C：切换窗口（时长 = B6 实测 + 缓冲）⛔

1. 公告维护窗口（低峰时段）。
2. drain：`sudo docker kill -s USR1 tokenkey`，轮询
   `sudo docker exec tokenkey wget -qO- localhost:8080/health/inflight` 至 `in_flight=0`。
3. 最终数据搬运（同 B6 的 dump→restore 命令；先 `DROP/TRUNCATE` 演练残留或对空库 restore）。
   行数对账：源/目标各跑
   `select count(*) from usage_logs union all select count(*) from users ...`。
4. ⛔ **切换**（写 SSM overlay + 投递 compose + force-recreate + stop 本机 PG + 验证，一条命令）：
   ```bash
   AWS_REGION=us-east-1 TK_DATA_PG_HOST=<PgEndpointAddress> \
   TK_DATA_PG_CLIENT_IMAGE=postgres:<major>-alpine \
     ops/stage0/cutover_data_layer_via_ssm.sh apply <prod-instance-id>
   ```
   脚本失败时自动：host 端还原文件 + 本地删 overlay 参数（next-boot 一致性）。
5. 烟测：`ops/stage0/post_deploy_smoke.sh` 全绿 + dashboard 正常 + 新请求产生 usage_logs。
6. 解除公告。

## 阶段 C+：reboot 一致性验证（窗口后择机）⛔

```bash
aws ec2 reboot-instances --instance-ids <prod-instance-id> --region us-east-1
```
验证（这是不变量 1 的实测，必须做）：
- `.env` 含 `DATABASE_HOST=<rds>` + `COMPOSE_PROFILES=localredis`（持久盘上的 .env 跨 reboot 保留）；
- `tokenkey-postgres` 容器**没有**被 systemd 拉起（localpg profile 未激活）；
- redis 容器正常、app healthy、smoke 绿。
> 注意：reboot 不重跑 bootstrap（UserData 仅首启）。bootstrap-从-SSM-overlay
> 重渲染路径由 B7 演练里的 instance replace 验证；prod 的下一次真实 replace
> （resize 等）按 resize SOP 走时顺带复验。

## 阶段 D：观察期与收尾

1. 观察 24h：错误率（ops_error_logs）、延迟、RDS CloudWatch（连接数 <180、
   CPU、FreeableMemory、FreeStorageSpace）——4 条告警已随数据栈带出。
2. pgdump timer 已自动改走 RDS（wrapper 读 .env），核对
   `/var/lib/tokenkey/pgdump/` 仍每 2h 出新文件且 >2KB。
3. **14 天后** ⛔：确认无回滚需求 → `sudo rm -rf /var/lib/tokenkey/postgres/*`
   （DLM 快照里仍有历史副本）。

## 回滚

- **任意时刻一条命令**：
  ```bash
  AWS_REGION=us-east-1 ops/stage0/cutover_data_layer_via_ssm.sh rollback <prod-instance-id>
  ```
  动作：删 overlay 参数 → 还原备份的 .env/compose → 本机 PG/Redis 拉起 →
  force-recreate tokenkey → 验证。本机数据卷一直在，秒级恢复。
- **代价声明**：切换之后落在 RDS 的写入（新用户/计费/用量）不会回放到本机库。
  观察期越晚回滚代价越大——这就是 24h 观察期要盯紧的原因。

## 已知锁定关系

- app 栈的 `VpcId` / `PrivateSubnetIds` / `AppSecurityGroupId` Exports 被数据栈
  Import 后**不可改名/删除**；改网络拓扑先 delete 数据栈（RDS 资源 Retain，不丢数据）。
- RDS `DeletionProtection=true` + `DeletionPolicy=Retain`：删栈不删库；真要删库
  需先关 DeletionProtection（刻意的两步门）。
