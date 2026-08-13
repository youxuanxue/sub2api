# usage_logs 日分区切换

该目录的 `usage_logs_daily_partition.py` 是唯一生产入口。它不会由应用启动或 migration
runner 自动执行，也不会复制历史明细。

先并发创建普通 `(request_id, api_key_id)` 查询索引，再在线准备并验证固定上界 CHECK：

```bash
python3 ops/migration/usage_logs_daily_partition.py prepare \
  --receipt /path/to/usage-prepare-receipt.json \
  --confirm tokenkey-prod-usage-daily-prepare-v1
```

如果 receipt 写入失败或决定取消 cutover，先用 `status` 读取
`legacy_upper_exclusive`，再用与该上界精确绑定的确认串撤销 CHECK：

```bash
python3 ops/migration/usage_logs_daily_partition.py abort \
  --receipt /path/to/usage-abort-receipt.json \
  --legacy-upper-exclusive '<legacy_upper_exclusive>' \
  --confirm 'tokenkey-prod-usage-daily-abort-v1:<legacy_upper_exclusive>'
```

`abort` 只允许在尚未 cutover、且 CHECK 带有 operator ownership 标记时执行；普通查询索引保留。

准备 receipt 会给出绑定上界的 `required_cutover_confirmation`。维护窗口内原样使用该值执行
短 catalog cutover：

```bash
python3 ops/migration/usage_logs_daily_partition.py cutover \
  --prepare-receipt /path/to/usage-prepare-receipt.json \
  --cutover-receipt /path/to/usage-cutover-receipt.json \
  --confirm '<required_cutover_confirmation>'
```

锁等待超过 5 秒、上界过期、CHECK 未验证、出现未知 incoming FK、父表仍有全局唯一索引或
切换后 legacy 行数小于 prepare receipt、或父表行数小于 legacy 行数时均 fail closed。账务幂等
仍由 `usage_billing_dedup` 负责。

## 分区维护一次性入口

`data_layer_partition_maintenance.py` 只面向固定的 `us-east-1` / `tokenkey-prod-stage0`，
不接受 target、instance、script 或 command 参数。steady state 下日常分区维护由
`OpsCleanupService` 负责；本入口仅在 cutover 后补洞或覆盖漂移时使用，每次仍须显式确认串与
独立审批。

命令形状固定为：

```bash
python3 ops/migration/data_layer_partition_maintenance.py run \
  --receipt /path/to/partition-maintenance-receipt.json \
  --confirm tokenkey-prod-partition-maintenance-v1
```

controller 只发送一次固定 SSM 命令，并轮询同一个 `CommandId`。只有远端严格分区维护、覆盖验证和
成功 heartbeat 全部完成后，才会原子创建本地 receipt；已有 receipt 不会被覆盖。该入口不授权删除、
cleanup release、部署、重启或配置修改。

## Lightsail Edge 迁移到 EC2

`edge-ec2-migration.sh` 是单 Edge 零数据丢失迁移入口。公开状态只有：

```text
prepared -> cutting_over -> observing -> stable
```

命令默认只打印计划；只有显式 `--execute` 才会调用 SSM 或写本地 state。每次线上
`--execute`、candidate provision、DNS 修改和唯一 owner 切换都需要在实际执行点单独审批。
本入口不删除 Lightsail、不批量循环 Edge，也不执行 DNS 修改。

### 前置门禁

1. 只迁移一个 Edge。同一 state 文件的本地执行锁串行化整条 controller 编排；迁移控制器、普通
   Edge deploy 和 candidate update 另共用每台 host 的远端 action lock。`.write-owner-locked`、
   `.target-write-owner-active`、`.target-proxy-retained` 分别守卫被冻结写端、已接管写入的目标和
   只保留回滚代理的目标。这些 marker 是内部写入守卫，不是公开状态。
2. action lock 不覆盖所有运维入口。迁移执行审批必须在整个窗口冻结该 Edge 的账号导入/编辑、
   Caddy/飞书/host unit 配置同步及其他会改写 host 或数据的操作；源冻结后 app、PostgreSQL、Redis
   和 Caddy 会物理停止，再生成最终快照。
3. EC2 candidate 必须由 `deploy-edge-stage0.yml` 的 `provision` 创建，并保持 app/Caddy 停止。
4. `auto` 路由仍必须只有 Lightsail 一个 deployable owner；EC2 仅为
   `migration_candidate=true`。进入 `stable` 后再用独立代码变更把 EC2 设为唯一 deployable
   owner，同时把 Lightsail 设为非 deployable。该变更不删除 Lightsail。
5. 本地 state 文件放在仓库外的受控目录并持久保留到本次迁移结束。不要把 state、预签名 URL、
   bundle 或执行输出提交到 git。
6. 正式 cutover 前，`prepare` 的完整演练耗时必须不超过 120 秒。控制器会再次机械校验；该上限只约束正常成功的向前切换。

`prepare` 在源端在线时验证 bundle checksum、成员 checksum、完整恢复、Compose 配置、镜像拉取和应用容器可创建，但不启动带真实账号数据的目标应用。源端在演练期间仍会写入，因此该阶段不比较跨时间点的 PostgreSQL、Redis 或普通文件内容摘要；冻结后的正式 `cutover` 才会执行逐表、Redis 和文件的完整对称校验，启动目标应用并验证健康，只有它能证明最终数据一致且写端可用。

### 一次性 OIDC addon

首次创建任何 EC2 candidate 前，使用管理员凭证部署独立 addon；这一步只建立受限 GitHub caller
policy 和 EC2 Edge CFN execution role，不创建 Edge 实例：

```bash
aws cloudformation deploy \
  --region us-east-1 \
  --stack-name tokenkey-cicd-ec2-edge-addon \
  --template-file deploy/aws/cloudformation/cicd-oidc-ec2-edge-addon.yaml \
  --capabilities CAPABILITY_NAMED_IAM
```

若实际 workflow 使用多个 OIDC role，显式传
`GitHubOidcRoleNames=role-a,role-b`；禁止猜 role 名。部署后用
`ops/stage0/check-cfn-live-drift.sh` 对该 stack 做 no-execute changeset 核对，确认 live 与 repo 无漂移，
再允许执行 `deploy-edge-stage0.yml` 的 `provision`。

### 机械解析迁移参数

以下命令只读 repository、CloudFormation 和 SSM。先设置单 Edge ID；不要批量循环：

```bash
export TK_MIGRATION_EDGE_ID=us4
export TK_MIGRATION_STATE_DIR=/secure/tokenkey-edge-migration
install -d -m 0700 "$TK_MIGRATION_STATE_DIR"

EC2_TARGET="$(jq -cer --arg edge "$TK_MIGRATION_EDGE_ID" \
  '.targets[$edge] | select(.migration_candidate == true)' \
  deploy/aws/stage0/edge-targets.json)"
LIGHTSAIL_TARGET="$(jq -cer --arg edge "$TK_MIGRATION_EDGE_ID" \
  '.targets[$edge] | select(.deployable == true)' \
  deploy/aws/lightsail/edge-targets-lightsail.json)"

export TK_MIGRATION_TARGET_REGION="$(jq -r .region <<<"$EC2_TARGET")"
export TK_MIGRATION_TARGET_STACK="$(jq -r .stack <<<"$EC2_TARGET")"
export TK_MIGRATION_DOMAIN="$(jq -r .domain <<<"$EC2_TARGET")"
export TK_MIGRATION_SOURCE_REGION="$(jq -r .lightsail_region <<<"$LIGHTSAIL_TARGET")"
export TK_MIGRATION_SOURCE_PREFIX="$(jq -r .ssm_prefix <<<"$LIGHTSAIL_TARGET")"
export TK_MIGRATION_SOURCE_INSTANCE_ID="$(aws ssm get-parameter \
  --region "$TK_MIGRATION_SOURCE_REGION" \
  --name "$TK_MIGRATION_SOURCE_PREFIX/ssm_managed_instance_id" \
  --query Parameter.Value --output text)"
export TK_MIGRATION_SOURCE_PUBLIC_IP="$(aws ssm get-parameter \
  --region "$TK_MIGRATION_SOURCE_REGION" \
  --name "$TK_MIGRATION_SOURCE_PREFIX/public_ip" \
  --query Parameter.Value --output text)"
export TK_MIGRATION_TARGET_INSTANCE_ID="$(aws cloudformation describe-stacks \
  --region "$TK_MIGRATION_TARGET_REGION" --stack-name "$TK_MIGRATION_TARGET_STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='InstanceId'].OutputValue" --output text)"
export TK_MIGRATION_TARGET_EIP="$(aws cloudformation describe-stacks \
  --region "$TK_MIGRATION_TARGET_REGION" --stack-name "$TK_MIGRATION_TARGET_STACK" \
  --query "Stacks[0].Outputs[?OutputKey=='PublicIP'].OutputValue" --output text)"
export TK_MIGRATION_COMMIT="$(git rev-parse HEAD)"
export TK_MIGRATION_STATE_FILE="$TK_MIGRATION_STATE_DIR/$TK_MIGRATION_EDGE_ID.json"
```

必须人工核对解析结果与 DNS 当前 A 记录一致，并确认 source 是 `mi-*`、target 是 17 位
`i-*`、commit 是计划执行的完整 40 位 SHA。任何空值或不一致都停止。

### 私有临时传输桶

迁移 bundle 包含数据库、账号、凭证和密钥，临时 S3 bucket 必须满足：

- S3 Block Public Access 全开，bucket/object 不允许 public ACL 或 public policy。
- 默认使用 SSE-S3 或 SSE-KMS；对象上传也显式指定相同加密方式。
- lifecycle 在迁移完成后短期删除对象，建议不超过 24 小时。
- helper、forward bundle、reverse bundle 使用三个不同 object key。
- 预签名 URL 使用满足演练和 cutover 的最短 TTL，只经环境变量传入。
- URL 不写 state、不写 shell history、不打印到日志；迁移完成后删除对象并撤销临时权限。

先经独立审批上传当前 commit 的 helper，并记录摘要：

```bash
export TK_MIGRATION_BUCKET=private-tokenkey-edge-migration
export TK_MIGRATION_BUCKET_REGION=us-east-1
export TK_MIGRATION_PREFIX="edge/$TK_MIGRATION_EDGE_ID/$(date -u +%Y%m%dT%H%M%SZ)"
export TK_MIGRATION_HELPER_KEY="$TK_MIGRATION_PREFIX/helper/edge_ec2_remote.py"
export TK_MIGRATION_FORWARD_KEY="$TK_MIGRATION_PREFIX/forward/bundle.tar.gz"
export TK_MIGRATION_REVERSE_KEY="$TK_MIGRATION_PREFIX/reverse/bundle.tar.gz"
export TK_MIGRATION_HELPER_SHA256="$(sha256sum ops/migration/edge_ec2_remote.py | awk '{print $1}')"

aws s3 cp ops/migration/edge_ec2_remote.py \
  "s3://$TK_MIGRATION_BUCKET/$TK_MIGRATION_HELPER_KEY" \
  --region "$TK_MIGRATION_BUCKET_REGION" --sse AES256
```

使用本地 boto3 为 helper GET、forward PUT/GET、reverse PUT/GET 分别生成 URL。下面代码只把
URL 放进当前进程环境；运行前关闭 shell tracing，运行后清除变量：

```bash
set +x
eval "$(python3 - <<'PY'
import os, shlex
import boto3

client = boto3.client("s3", region_name=os.environ["TK_MIGRATION_BUCKET_REGION"])
bucket = os.environ["TK_MIGRATION_BUCKET"]
ttl = 3600
items = {
    "TK_MIGRATION_HELPER_GET_URL": ("get_object", os.environ["TK_MIGRATION_HELPER_KEY"]),
    "TK_MIGRATION_FORWARD_PUT_URL": ("put_object", os.environ["TK_MIGRATION_FORWARD_KEY"]),
    "TK_MIGRATION_FORWARD_GET_URL": ("get_object", os.environ["TK_MIGRATION_FORWARD_KEY"]),
    "TK_MIGRATION_REVERSE_PUT_URL": ("put_object", os.environ["TK_MIGRATION_REVERSE_KEY"]),
    "TK_MIGRATION_REVERSE_GET_URL": ("get_object", os.environ["TK_MIGRATION_REVERSE_KEY"]),
}
for name, (method, key) in items.items():
    url = client.generate_presigned_url(
        method, Params={"Bucket": bucket, "Key": key}, ExpiresIn=ttl
    )
    print(f"export {name}={shlex.quote(url)}")
PY
)"
```

`cutover --execute` 在冻结 Lightsail 前强制校验 helper、forward 和 reverse 五条 URL 全部存在，
确保 EC2 接受写入后仍能反向同步回滚。

### 公共参数数组

首次 `prepare` 用 `manifest-digest=auto`，让源端 manifest 机械绑定真实摘要：

```bash
MIGRATION_ARGS=(
  --state-file "$TK_MIGRATION_STATE_FILE"
  --edge-id "$TK_MIGRATION_EDGE_ID"
  --source-region "$TK_MIGRATION_SOURCE_REGION"
  --source-instance-id "$TK_MIGRATION_SOURCE_INSTANCE_ID"
  --source-public-ip "$TK_MIGRATION_SOURCE_PUBLIC_IP"
  --target-region "$TK_MIGRATION_TARGET_REGION"
  --target-instance-id "$TK_MIGRATION_TARGET_INSTANCE_ID"
  --target-eip "$TK_MIGRATION_TARGET_EIP"
  --domain "$TK_MIGRATION_DOMAIN"
  --commit "$TK_MIGRATION_COMMIT"
  --manifest-digest auto
)
```

任何命令不带 `--execute` 都是 plan-only：

```bash
ops/migration/edge-ec2-migration.sh prepare "${MIGRATION_ARGS[@]}"
```

取得独立执行审批后才运行：

```bash
ops/migration/edge-ec2-migration.sh prepare "${MIGRATION_ARGS[@]}" --execute
```

成功后从原子 state 读取真实摘要，并重建后续参数。后续禁止继续传 `auto`：

```bash
export TK_MIGRATION_MANIFEST_DIGEST="$(jq -er .binding.manifest_digest "$TK_MIGRATION_STATE_FILE")"
MIGRATION_ARGS=(
  --state-file "$TK_MIGRATION_STATE_FILE"
  --edge-id "$TK_MIGRATION_EDGE_ID"
  --source-region "$TK_MIGRATION_SOURCE_REGION"
  --source-instance-id "$TK_MIGRATION_SOURCE_INSTANCE_ID"
  --source-public-ip "$TK_MIGRATION_SOURCE_PUBLIC_IP"
  --target-region "$TK_MIGRATION_TARGET_REGION"
  --target-instance-id "$TK_MIGRATION_TARGET_INSTANCE_ID"
  --target-eip "$TK_MIGRATION_TARGET_EIP"
  --domain "$TK_MIGRATION_DOMAIN"
  --commit "$TK_MIGRATION_COMMIT"
  --manifest-digest "$TK_MIGRATION_MANIFEST_DIGEST"
)
```

state 会绑定 Edge、source、target、EIP、domain、commit 和 manifest；任一漂移都会在远端调用前失败。
完整 cutover 结束后的重复调用是 noop。若控制进程在远端动作完成与 checkpoint 写入之间中断，
`cutover` 会拒绝继续；先 plan 并审批执行 `rollback`，确认回到 Lightsail 后重新 prepare/cutover，
不要删除或手改 state 猜测续跑。每台 host 同一时刻只允许一个迁移动作；若 rollback 返回
`another migration action is still running`，说明中断前提交的 SSM action 尚未结束，等待其进入终态后
原样重试 rollback，禁止并发恢复。

### Cutover、DNS 与观察

先打印正式切换计划；再次取得独立审批后才执行：

```bash
ops/migration/edge-ec2-migration.sh cutover "${MIGRATION_ARGS[@]}"
ops/migration/edge-ec2-migration.sh cutover "${MIGRATION_ARGS[@]}" --execute
```

控制器从源端 drain 开始计算正常向前切换的 120 秒上限，依次完成最终逻辑 dump、Redis 持久化、
恢复与对称校验、EC2 启用和旧 Lightsail IP 的 Caddy 代理。超时或校验失败立即停止向前推进：

- EC2 接受写入前失败：直接恢复 Lightsail。
- EC2 接受写入后失败：先冻结 EC2，完整反向同步并验证，再恢复 Lightsail。

异常回滚不承诺在同一个 120 秒内完成。数据完整性优先于恢复耗时；反向同步和校验未完成前，
禁止恢复 Lightsail 写入或直接修改 DNS。

成功结果只打印精确 DNS A 记录变更和 confirmation token，`executed=false`。人工完成 DNS 变更并
从权威解析确认目标 EIP 后，先 plan，再经独立审批记录 `observing`：

```bash
DNS_CONFIRM="confirm-dns-cutover:$TK_MIGRATION_EDGE_ID:$TK_MIGRATION_DOMAIN:$TK_MIGRATION_TARGET_EIP"
ops/migration/edge-ec2-migration.sh observe "${MIGRATION_ARGS[@]}" \
  --confirm-dns "$DNS_CONFIRM" --observed-dns-ip "$TK_MIGRATION_TARGET_EIP"
ops/migration/edge-ec2-migration.sh observe "${MIGRATION_ARGS[@]}" --execute \
  --confirm-dns "$DNS_CONFIRM" --observed-dns-ip "$TK_MIGRATION_TARGET_EIP"
```

`observe --execute` 会每 30 秒机械复验 EC2 本机 HTTPS 健康和旧 Lightsail IP 的 Caddy 代理链路，
连续通过至少 600 秒才记录完成；任一探测失败或控制进程中断，下一次 `observe --execute` 会从完整
窗口重新开始。CloudWatch/飞书告警仍需在执行审批时人工确认正常。最终数据、账号和凭证差异已由
冻结后的 restore 对称校验门禁。观察完成后，plan 并经独立审批标记稳定：

```bash
ops/migration/edge-ec2-migration.sh mark-stable "${MIGRATION_ARGS[@]}"
ops/migration/edge-ec2-migration.sh mark-stable "${MIGRATION_ARGS[@]}" --execute
```

`stable` 不授权删除 Lightsail。完成 EC2/Lightsail deployable owner 的独立代码变更并解除部署冻结后，
Lightsail 继续保留为受控回滚资源；退役另行审批。

### 手工回滚

任何已由 EC2 接受写入的状态都禁止直接改 DNS 回 Lightsail。先 plan，再经独立审批执行第一段：

```bash
ops/migration/edge-ec2-migration.sh rollback "${MIGRATION_ARGS[@]}"
ops/migration/edge-ec2-migration.sh rollback "${MIGRATION_ARGS[@]}" --execute
```

第一段会冻结 EC2、反向同步并验证、恢复 Lightsail，并让旧 EC2 EIP 在等待 DNS 回切期间代理到
已恢复的 Lightsail，然后只打印 DNS 回切变更。此时 EC2 的回滚代理 marker 会同时阻断 candidate
update 和普通 Edge deploy，避免旧 EC2 被重新启动成写入端。人工完成 DNS 回切并确认解析到原 Lightsail IP 后，
执行第二段确认：

```bash
DNS_ROLLBACK_CONFIRM="confirm-dns-rollback:$TK_MIGRATION_EDGE_ID:$TK_MIGRATION_DOMAIN:$TK_MIGRATION_SOURCE_PUBLIC_IP"
ops/migration/edge-ec2-migration.sh rollback "${MIGRATION_ARGS[@]}" --execute \
  --confirm-dns "$DNS_ROLLBACK_CONFIRM" --observed-dns-ip "$TK_MIGRATION_SOURCE_PUBLIC_IP"
```

DNS 回切确认后，EC2 暂时继续代理到 Lightsail，给仍缓存旧 EC2 EIP 的请求排水。确认该旧 EIP
排水窗口结束后，再执行一次同一命令释放目标为停止应用的 candidate；在此之前 `prepare` 和
candidate update 都会 fail closed：

```bash
ops/migration/edge-ec2-migration.sh rollback "${MIGRATION_ARGS[@]}" --execute
```

迁移或回滚完成后，清除所有 `TK_MIGRATION_*_URL` 环境变量，并按临时桶 lifecycle/清理审批删除
helper、forward 和 reverse 对象。保留受控 state，不保留预签名 URL 或过程日志到仓库。
