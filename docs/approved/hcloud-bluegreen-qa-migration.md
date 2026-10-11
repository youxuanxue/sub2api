---
title: hcloud 全栈蓝绿与独立 QA 去 AWS
status: approved
approved_by: "user（本会话：全栈 hcloud、QA 去 AWS、uk1 限额及普通内容不额外加密）"
created: 2026-10-11
owners: [tk-platform]
risk: high
scope: "prod/Edge 蓝绿发布、QA Worker、任务队列、raw/Bundle 存储及历史迁移"
---

# hcloud 全栈蓝绿与独立 QA 去 AWS

## 网关发布验收更新（2026-10-11）

1.8.284 已完成 prod + 活 Edge 蓝绿升级、排空和烟测（发布修复 #2571）。
下文首次蓝绿待验收描述为当时快照，网关部分已完成；QA 正式发布、数据/队列迁移和
真实 UI 验收仍未完成。现网状态与 AWS 保留依赖见 `deploy/hetzner/README.md`。

## QA uk1 运行准备（2026-10-11，尚未切生产）

已创建受删除保护的私网 `tokenkey-qa`（10.77.0.0/24），仅 uk1 接入 10.77.0.3；
prod 尚未接入，原默认路由保持不变。`deploy/hetzner/qa/runtime.json` 记录非秘密运行坐标。
独立 PostgreSQL 与 worker 已在 uk1 启动，队列为空且保持 paused；生产 producer、旧 ECS
worker、SQS 与 AWS 对象存储继续工作。不得把目标准备完成当成正式迁移完成。

按用户后续批准，S3 复用既有项目凭据，分别通过私有文件挂载；队列登录角色仍独立。
实际父 cgroup 已读回确认总量 1 CPU / 2 GiB、swap=0、256 PID；底层磁盘读写各限
10 MiB/s、100 IOPS。scratch 与队列各使用预分配 4 GiB ext4 loop filesystem，带
nodev/nosuid/noexec，独立 UID 和只读根文件系统。

`verify_hcloud_runtime.py` 在线验证通过实际 cgroup 归属、容器权限、硬容量文件系统、
consumer TLS/角色权限，以及宿主机、Edge 数据服务、元数据、非白名单公网和反向
Edge→QA 队列拒绝。原网关容器启动时间未变化，连续健康检查保持通过；这不替代
生产业务延迟、峰值负载和蓝绿并发验收。

AWS 只读盘点存在约 228 GB raw 对象。预拷贝 owner 已通过单元测试及 Hetzner 专用
验证桶的 9 MiB multipart、SHA256 回读、重跑验证；合成对象已清理，尚未复制生产数据。
目标文件系统与容器尚未启用开机自动恢复；备份恢复、动态出站地址维护、发布协调、
保留期清理、生产冻结/迁移对账、真实 UI 和首次切换验收仍须完成。

## 会话授权与实施边界

用户已同意补齐 prod 和 Edge 的全栈 hcloud 蓝绿部署，并要求 QA worker、存储和队列
一起迁出 AWS。随后批准先验证共用 uk1，以及下述资源限额和直接开通所需云资源。
用户明确 QA 内容属于普通/一般机密，不需要额外加密；本方案因此采用私有 Object Storage、
HTTPS、最小权限凭据和既有用户下载授权，不引入 SSE-C、自管加密密钥或下载请求头。

本阶段交付可选 PostgreSQL 队列、排空接线、Hetzner 条件写适配、资源配置与合成验证。
已开通对象存储；完整 QA 运行包、身份/网络、磁盘配额、正式迁移和首次蓝绿验收仍需实现。
生产数据尚未迁移，未切换生产队列，也未启用新的 QA worker。

蓝绿沿用 [Stage0 部署基线](deploy-stage0-workflow.md) 的状态机和
[Edge 安全决策](edge-bluegreen-release-safety.md)。QA 的供应商与队列替换涉及
[QA lifecycle](design-prod-qa-24h-s3-lifecycle.md) §7/§8/§13 的存储与权限边界，
下述差异必须经过实际权限、恢复与迁移验收，不能仅将 S3 URL 替换成另一个 endpoint。

本方案的“QA 去 AWS”指 QA 运行与数据链路不再依赖 ECS、SQS、S3、KMS、CloudTrail。
现有发布控制面 SSM Hybrid、GitHub OIDC，以及非 QA 的备份/媒体存储仍是不同范围；
若要求整个系统零 AWS 依赖，需要同时替换这些控制面与存储，不能把本方案称为已完成该目标。

## 已核实的起点

检查基线包含 #2551（`db8619b40`），提交前已继续快进至
`origin/main` 的 `12efff54d`（#2553，Caddy 同步蓝绿修复）。
正式 prod DNS 指向 Hetzner；活舰队五节点 `/health` 均为 200、SSM 均在线
（节点清单见 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)）。
prod 运行 `1.8.283`，容器为 `tokenkey`，尚无 active color。
四边容量探针均合格；此次低流量 canary 结果为 uk1，执行时必须重新计算。

已确认的代码缺口：

- #2551 已接线 Hetzner upgrade/rollback/smoke，复用共享蓝绿 primitive；线上首次切色尚未执行。
- prod 具有 legacy → blue 的首次迁移能力，但 prepare/replay 拒绝 legacy 布局。
- 后续发布链路已补 Hybrid 临时 AWS 凭证目录挂载与 Volume 门禁；首次线上切色尚未验收。
- QA workflow 仍从 EC2 解析网络并部署 Fargate；本分支已补 PostgreSQL adapter，默认仍为 SQS。
- PostgreSQL 队列入口不加载 AWS 凭据；SQS 分支保留旧配置以兼容现网。
- #2551 已对齐 prod 目标矩阵及正式 DNS 文档；不回填未执行的线上蓝绿验收。

## 推荐架构

### Gateway / Edge

prod 与四边复用 `ops/stage0/deploy_via_ssm_bluegreen.sh`。平台适配只解析目标与
SSM region/profile；不复制切流、排空或回滚状态机。前端随 gateway 镜像发布。

PostgreSQL/Redis 保持单份数据，Caddy 只热切 app upstream。Gemini worker 保持
独立容器与既有会话目录，镜像更新使用它自己的发布入口；不随每次 gateway 切色重建。
QA maintenance 保留独立 pin 与 lifecycle 锁。

首次升级继续 legacy → blue：旧进程服务到新进程健康、Caddy 切流并观察成功后才排空。
这次保留 prepare 对 legacy 的拒绝，避免把“准备候选”偷偷变成第一次正式切流。
首次迁移验收后即可使用既有 prepare/replay/promote。

部署前验证 arm64 manifest、SQL 向后兼容、内存/磁盘、真实目标身份；prod 与 Edge
明确传 profile。新增 hcloud prod Volume 挂载验证及 systemd `RequiresMountsFor`，
避免卷未挂载时在根盘创建一套空数据。蓝绿配置保留需要的凭证/对象存储配置及共享目录，
不输出 secret。重启后只启动已确认活动色；不确定状态遵守现有保留两色、人工恢复契约。

发布顺序仍为 canary full → prod full → 其余 Edge infra；所有节点使用同一发布镜像。
切流前失败清理候选并保留旧色；切流后失败保留证据和旧色，不自动反复切换。
回滚通过原入口部署 previous tag，并执行相同兼容性与健康门禁。

### QA Worker 与 PostgreSQL 队列：先验证共用 uk1

根据用户后续选择，先验证在现有 uk1 上运行独立 QA 容器及队列，暂停新增专用主机。

用户已批准的资源预算由 `deploy/hetzner/qa/resource-policy.json` 单一拥有：
worker 0.75 CPU / 1.5 GiB，queue 0.25 CPU / 512 MiB；合计 1 CPU / 2 GiB，单任务并发。
`ops/qa/colocation_resources.py` 生成父级 `tokenkey-qa.slice` 与 Compose resource override，
同时限制子容器与总量，禁止 swap。该 override 不是完整发布清单；独立 UID/存储/凭据/
TLS/出站白名单通过后才能接线运行。4 GiB scratch 是硬要求，普通 bind mount/volume
没有容量配额，不能用此 override 声称已实现磁盘隔离。Edge 蓝绿发布须先 pause queue 并排空。

独立主机是隔离与故障域的备选，不能把共用主机容器称为“绝对隔离”。
普通 runc 容器共享宿主内核和 Docker daemon；主机管理员仍能读取容器数据，
主机故障/重启也会同时影响 Edge 与 QA。若独立内核/故障域是硬要求，共用方案不得上线。

生产共用方案须使用独立服务身份、数据卷、凭据、网络及发布入口。QA 不得获取 Docker
socket、Edge/prod 数据库凭据、Edge 数据目录或 host PID/network namespace。
非 root、只读根文件系统、drop ALL capabilities、no-new-privileges、seccomp/AppArmor
以及 CPU/memory/PID/scratch/log/IO 配额均为必要条件；普通独立 Docker bridge
不能替代经过实测的出站白名单，`internal` network 也不能自动证明所有 host 服务不可达。
访问队列与对象存储的实际路径、反向 Edge→QA 访问、主机元数据与公网出口均需拒绝测试。
还须验证 QA 峰值与 Edge 蓝绿升级并发时的网关表现；当前资源快照不证明容量验收。

QA 服务运行独立 PostgreSQL 队列库和 Bundle worker。队列不放入 prod/Edge 用户库，
worker 不获取 prod 用户库凭据。prod 通过受限私网 + TLS 访问队列；只有
enqueue 数据库角色，worker 只有 claim/ack 权限，迁移/schema owner 单独管理。
不暴露公网 PostgreSQL，不使用开发环境 trust 认证。

队列复用 `bundle.JobQueue` / `bundle.JobConsumer` 接口，保留现有 immutable JobSpec、
receipt、failure 和 `traj-ssot` projector。新 adapter 负责：

- `spec_key` 唯一约束去重；只有已持久化并验证的 spec 才入队。
- 领取先串行锁定 control 行，再原子 `FOR UPDATE SKIP LOCKED`；全队列最多一个有效 lease，receipt 每次重投更换。
- 一小时可见性租约与最多三次投递，向现有 worker 的 terminal receipt 规则对齐。
- ack 必须匹配当前且未过期的 receipt；旧 worker 不能确认新 worker 的投递。
- 未确认的毒消息进入 durable dead-letter 状态，保留领取次数及 dead 状态；无静默清空。
- enqueue/ack 失败可重试；完成以已校验对象 receipt 为准，不宣称 exactly-once 执行。
- 备份包含队列与 dead-letter；恢复后过期 lease 可重投，已完成 job 依靠 receipt 去重。

worker 蓝绿不是 HTTP upstream 切换：候选先做配置/存储/队列连通验证，
旧 worker 停止新领取并完成在途任务，之后启用新 consumer；超过排空预算保留旧进程并失败。
不直接 SIGKILL 正在生成的 Bundle。升级复用独立 QA 发布入口与 `prod-qa-lifecycle` 锁。

### Hetzner Object Storage：私有存储与普通签名下载

raw 与 user Bundle 分别使用私有 Hetzner bucket，保留现有 key/prefix、immutable create、
ETag CAS、checksum、restore verification、短生命周期和用户授权。对象内容不经 gateway
大体积代理，浏览器仍使用普通短期签名链接，不获得存储 access/secret key。

用户已接受 QA 普通内容不额外进行 SSE-C/应用层加密。这里的“无额外加密”不关闭
HTTPS、私有桶、身份和应用授权，也不推断供应商内部磁盘是否加密。现有 AWS raw 的
KMS 与 Bundle 的 SSE-S3 属源端机制，源对象迁移时仍需原身份正常解密读取。
目标端不发送 SSE-C/KMS/SSE-S3 请求头；不新增密钥管理或前端下载协议。

官方 [支持列表](https://docs.hetzner.com/storage/object-storage/supported-actions/)
不支持 versioned bucket 的条件 PUT/DELETE；因此目标 bucket 不启用 versioning。
实际供应商的条件写、multipart、Content-Encoding、CORS、签名过期和权限拒绝都须验证。
Hetzner 的 If-Match 值需去掉返回 ETag 的外层引号，适配只对官方 Hetzner HTTPS endpoint
生效；其他 S3 的行为保持不变，错误/过期 ETag 仍必须返回冲突。

用户在后续实施会话明确批准“用之前的 s3 凭据就行”：producer、worker、recovery
复用现有 Hetzner 项目级 S3 凭据，通过各组件独立的私有只读文件注入，不要求另建三组
S3 身份。该凭据拥有项目桶读写权限，不能宣称 worker 在存储服务端只读 raw，
也不能宣称三者具备 S3 身份隔离。容器、挂载、队列角色、网络与用户授权仍须隔离；
共享凭据轮换须协调三个组件。raw 永不直接签发给普通用户；用户只能
获得经既有鉴权通过的 Bundle 签名 URL。过期对象清理与独立审计继续由 QA lifecycle 承载。
Hetzner 不提供 AWS CloudTrail/KMS/VPC Endpoint 的同等服务，本方案不宣称审计等价。

## 数据与任务迁移

存储迁移只复制既有对象，不移动/重排 QA 数据库分区；原 lifecycle 的 seal、watermark、
restore-before-DROP 与 terminal gap 授权继续适用。原有历史 Bundle 必须仍可访问。

1. **离线准备**：建目标身份和私有存储，在合成数据上验证私有访问、授权与对象契约。
   QA 主机队列/worker 的发布与恢复在隔离环境通过；此时不消费 live SQS。
2. **预拷贝**：按对象清单复制 raw 与仍在保留期内的 Bundle，逐对象核对明文 size/SHA256。
   保留 Content-Type/Encoding、逻辑 key、时间窗口与原始到期时刻；复制时间不能重置保留期。
   ETag 不是跨供应商 checksum，CAS 与本地控制面引用必须做语义核对。
3. **冻结 QA 窗口**：暂停新 Bundle 入队及 archive 写入，持久暂停 DROP；gateway 推理继续，
   capture 保持写本地分区。不能用停采集掩盖积压。停旧 worker 领取并排空在途任务。
4. **封账/增量**：复制最后的不可变对象及最终 commit，校验 raw shards 可恢复。
   把旧队列与 DLQ 逐条落入持久迁移台账后才确认源消息；台账以 spec key 去重，
   保存 origin/attempts 状态。SQS approximate count=0 不能单独证明任务迁完。
   结合 producer 冻结水位、inflight 排空和 job spec/receipt/failure 全量对账证明完整性。
5. **切目标**：一次更新 producer、maintenance 与 worker 的存储/队列坐标和凭据版本。
   新 worker 只消费目标库，生产者不双写；已完成任务由 receipt 验证去重。
6. **验收**：恢复新任务，真实 UI 列表/详情/会话 ZIP、旧窗口访问、权限拒绝、QA canary、
   raw restore 和重启恢复全部通过，才写 verified component receipt 并解除 DROP pause。
7. **退出 AWS**：旧存储、队列迁移台账与 worker 配置先只读保留；等旧签名 URL 过期、
   新目标验收和备份恢复成功后另批退役。新业务运行不得再依赖旧 AWS 对象。

任一步失败保持 DROP pause 与原数据。新目标未写入前可撤销坐标切换；写入后若要回旧目标，
必须先冻结 QA、反向复制新对象并对账，再迁回任务。禁止裸切回旧存储或删除未确认消息。

## 实现顺序与 owner

1. Hcloud gateway/Edge workflow 接线与首次蓝绿验收：现有 Stage0 dispatcher、resolver、
   `deploy_via_ssm_bluegreen.sh`、smoke、canary/rollout owner。
2. 队列 adapter 与隔离角色、worker 排空、独立 QA 主机运行包：`qa/bundle` 与
   `cmd/server/qa_bundle_worker.go`；替换 SQS 接线，不复制 JobSpec/publisher。
3. 对象凭据、授权下载及恢复：`qa/archive/store.go`、`qa/service_bundle.go`、
   共享 QA UI owner。S3 SDK 只作协议客户端，不依赖 AWS 服务。
4. QA 发布与迁移：`deploy-qa-bundle.yml`、`ops/qa`、`prod_release_plan/state` 和维护 pin。
   发布解析不再用 AWS CFN outputs 作为 QA runtime 真相。
5. Story、sentinel/preflight 与运维文档随实现更新；关键 owner 必须防上游覆盖。

生产实现集中同一工作分支，按已批准的共用资源、队列和普通私有存储方案推进。
最终合并、付费 provision、数据复制/正式切换按门禁验收执行；AWS 资产退役仍需单独授权。

## 验证计划与本地原型

PostgreSQL queue 的执行 owner 为 `deploy/hetzner/qa/queue.sql` 与
`backend/internal/observability/qa/bundle/queue_postgres.go`；producer、worker、canary
共享 `OpenJobQueue` 工厂。它只接受独立 `tokenkey_qa_queue` 库与 `verify-full` TLS URL，
DSN 从私有文件读取，禁止超级用户/owner 身份。不会自动创建 schema 或复用 prod 用户库。
SQL owner、producer、consumer 角色分离，业务角色不能直读写表或改变 pause 状态。

运行 `cd backend && go test -tags=integration ./internal/observability/qa/bundle -run TestUS062 -count=1`。
测试自动创建私有临时 PostgreSQL（本机具备工具时仅 Unix socket，否则 Testcontainers），
不接受外部 DSN；使用真正的受限登录角色验证权限、重复入队、并发单 lease、过期/旧 ACK、
三次失败保留、暂停领取/在途完成以及重启恢复。原独立 SQL 原型已由实际 owner 测试替换。

Worker 的 `RunOneDraining` 在停止信号后终止领取，已领取任务有 55 分钟完成上限，
短于一小时 lease；资源 override 留 56 分钟停止窗口。现有 `RunOne` 的取消行为不变。
此实现与本地测试不代表线上存储、worker 发布或 UI 已验收。

### uk1 共用隔离验证（2026-10-11）

只读入口：`ops/qa/probe_hcloud_colocation.sh`，经既有 `run-probe.sh` 发送，
强制核对目标 `mi-05d6c85a068e08a3c`。仅输出运行时与资源字段，不输出环境变量/凭据。
实测主机为 `tokenkey-edge-uk1-hz-cax21`，aarch64、4 CPU，约 7.13 GB 可用内存、
69.51 GB 根盘余量；cgroup v2、AppArmor 与 seccomp 已启用。
`/dev/kvm` 不存在，已安装的运行时只有 runc/containerd runc；因此没有已就绪的
独立内核沙箱。此结论不推断云厂商永远不支持其他虚拟化方案。

有界实验入口：`ops/qa/verify_colocation_sandbox.sh`，显式
`QA_COLOCATION_SYNTHETIC_OPT_IN=1` 后才运行。复用已安装的不可变 gateway image，
覆盖应用 entrypoint，不拉取镜像、不启动应用；临时容器不挂载主机目录、不注入凭据，
仅 loopback，96 MiB 内存、0.25 CPU、32 PID、1 MiB 临时目录，结束后删除。
该实验只验证具体容器边界，不能视为 QA 生产连通/迁移验收或共享内核安全证明。

实测合成实验通过：non-root、capabilities 清零、禁止提权、seccomp、AppArmor enforce、
根目录写入返回只读文件系统错误、无 host mounts/Docker socket、仅 loopback 且外连失败、
额外挂载被拒绝、临时数据可写且超过 1 MiB 返回写入失败。CPU/memory/PID 配额读取
容器内 cgroup 验证一致，**未做这些配额的耗尽压力测试**。实验容器完成后已删除；
执行前后正式 `/health` 均为 `{"status":"ok"}`，不据此推断所有业务延迟不受影响。

**当前判定：容器边界原型通过；“绝对隔离”不满足；生产共用门禁未通过。**
该主机实验未接入生产 QA 凭据、队列或数据，没有创建付费资源或更改现有 Edge 容器配置；
随后开通的 Object Storage 资源见下文。
仍需真实存储/队列 allow/deny、反向访问拒绝、资源压力/故障恢复、蓝绿并发验证。
现有 gateway/PG/Redis 容器没有 CPU/memory 上限，QA 自身上限不能证明双向资源独立。

实施验收必须增加：

- Gateway：legacy 首切、重复升级、prepare 不切流、rollback、坏镜像/容量不足/断线/排空超时、
  Caddy 持久状态不一致、卷未挂载、重启保留数据和 Gemini/QA 配置。
- Queue：真实 PostgreSQL 多消费者、连接失败/重启、重复提交、租约边界、毒消息、worker
  排空和快照恢复；最小权限角色不得读 prod 用户库或越权操作队列。
- Storage：真实供应商条件写与错 ETag 拒绝、私有桶匿名访问拒绝、共享凭据边界记录、应用授权隔离、multipart、
  checksum/Content-Encoding、CORS、签名过期及跨用户/跨 raw 域拒绝。
- Migration：中途失败重跑、源对象变化、缺对象/坏 checksum、队列 inflight、未知 envelope、
  历史 terminal gap 保持、写后回退对账和 DROP pause 不被提前解除。
- UI：Playwright 登录 QA 面板，读取旧/新 Bundle 并下载检查 session ZIP；超大导出有界内存，
  取消、断网、刷新及过期刷新复用共享 owner。
- Operations：完整 preflight、相应 Go/Python/浏览器测试、目标提交 CI，以及真实 canary。

### uk1 资源压力验证（2026-10-11）

`verify_colocation_resources.sh` 通过 `run-probe.sh` 调用 Python 验证 owner；
SSM CommandId `4fcaee5f-f422-4554-bdb2-57d37ae30a55` 成功。临时父 slice 的
CPU/memory/swap/PID 上限及两个子容器实际归属均通过，CPU 压力触发限流；
queue 在 512 MiB 限额内尝试分配 1 GiB，进程被 OOM kill 且 memory.events 计数增加。
这验证了 QA 队列容器的内存边界，没有对 worker 做 1.5 GiB 耗尽或验证 4 GiB 磁盘配额。
实验前后正式 `https://api-uk1.tokenkey.dev/health` 正常。

实验前基线 CommandId `2094b1a9-48e7-44f4-a9f2-3223092dd418`，
独立后查 `8e46d5c8-6810-4f0f-a71a-ab026b2b896f` 确认
`synthetic_containers=[]`、`synthetic_slice_files=[]`，现有服务的
started_at/restart_count 与实验前一致。未启动真实 QA worker、未接入生产凭据或数据。

### Object Storage 开通与兼容验证（2026-10-11）

用户授权开通，并在 Hetzner Console 创建 S3 凭据后提供本机环境变量名。
操作仅读取 `HCLOUD_S3_Access_Key` / `HCLOUD_S3_Secret_Key`，未打印密钥或写入仓库。
hcloud v1.70.1 无 S3 凭据管理命令；bucket 通过供应商 S3 API 创建，位于 fsn1：

- 验证：`tokenkey-qa-verify-fsn1-dd2ca9019b22`。
- raw：`tokenkey-qa-raw-fsn1-dd2ca9019b22`。
- Bundle：`tokenkey-qa-bundles-fsn1-dd2ca9019b22`。

专用验证脚本 `ops/qa/probe_hcloud_object_storage.py` 按最终方案实测私有/无版本 bucket、
普通对象读写、匿名读取拒绝、不可变创建冲突、错 ETag 拒绝、正确 ETag 更新、
multipart 上传与 SHA256 校验、无需额外请求头的签名 GET、CORS OPTIONS 和过期签名拒绝。所有合成对象和 multipart 均清理；
保留私有空 bucket，未迁移生产对象或任务。此为 S3 协议集成验证，不是浏览器 e2e。

发现必须显式适配的供应商差异：PUT 返回的 ETag 带引号，但 If-Match 条件 PUT
要求不带引号；带引号时正确 ETag 也返回 412。普通对象已复现并由 Go store 做 endpoint 限定适配。
CORS 配置在供应商前端间异步生效，配置后须等待实际预检成功。
Go archive store 已对官方 Hetzner HTTPS endpoint 加入 If-Match 适配，普通 S3 回归由同一 HTTP 行为测试覆盖；仍须完整运行路径验收。

按后续用户批准复用项目级 S3 凭据，队列 producer/consumer/bootstrap 身份仍须分离；
尚未把本机项目级凭据注入任何线上容器。跨用户和跨 raw 域拒绝、浏览器下载回归、历史对象迁移与备份恢复仍待运行验收。

## 上线判定

资源开通、队列本地通过和供应商合成测试通过均不等于完成生产迁移。
必须补齐独立队列身份及私有凭据文件、私网/TLS、scratch 硬配额、备份恢复、迁移对账、真实用户路径
以及 gateway/QA 蓝绿协调验证后，才允许正式切换。不得删除未确认迁完的 AWS 资产。

## 后续发布链路实现（待发布验收）

`qa-runtime-environment.sh` 共享本地 renderer 与远端的队列校验契约，支持 PostgreSQL
DSN 文件；蓝绿 compose 与独立 maintenance pin 传递 endpoint、path-style 及凭据文件路径。
`bluegreen_host_runtime.py` 验证凭据文件与挂载，两个颜色与 maintenance 使用同一 mount owner。
QA 存储 `credentials_file` 为 0400/0600 JSON，字段仅为 `access_key_id`、`secret_access_key`；
显式文件缺失、格式错误或与 inline credentials 混用时拒绝，不回退到进程 AWS 身份。
QA 文件分别挂到 `/run/secrets/tokenkey-qa/{bundle.json,raw.json,queue.dsn,queue-ca.pem}`，
不会把凭据内容传入 Compose/SSM 环境变量。私钥/DSN 文件归服务 UID 1000 所有。
单文件 bind mount 与进程初始化缓存要求轮换后重新准备/重启对应组件。
发布在改写宿主配置之前建立回退备份；legacy 升级、蓝绿升级与 prepare 的候选准备失败
均恢复该配置，避免把未通过凭据校验的配置留给下一次重启。

hcloud prod 在任何主机写入与 Docker 调用前验证 `/var/lib/tokenkey` 为真实 Hetzner ext4
Volume 挂载。首次蓝绿安装同时给 Docker 和 tokenkey systemd unit 添加 RequiresMountsFor
及 ExecStartPre 校验，防止 Docker 的自动重启先于应用 unit 在根盘启动空数据层。
Hybrid 临时 AWS 身份继续通过 `/var/lib/tokenkey/aws` 目录只读挂载，支持原子文件轮换；
QA 自身使用文件身份，不覆盖仍访问 AWS 的媒体/telemetry 身份。

`deploy/hetzner/qa/producer.json` 仅包含非秘密坐标；resolver 的 Hetzner 分支不请求 CFN。
`deploy-stage0` 默认仍使用 AWS 坐标，只有显式设置仓库变量 `QA_RUNTIME_PROVIDER=hetzner`
才选择新坐标。**该变量不是迁移命令，不能在正式迁移步骤之前切换。**
普通 gateway deploy 拒绝改变宿主队列后端；已迁移 PostgreSQL 的对象/队列坐标也必须与
宿主一致。迁移 owner 先在冻结/对账后更新宿主，再使发布入口选择相同坐标。
独立 QA workflow、uk1 worker/queue 正式运行包、私网/TLS、scratch 硬配额、备份恢复及
任务/对象迁移仍需完成；本节不表示 QA 已去 AWS，也不表示生产蓝绿首切已通过。
