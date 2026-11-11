# prod 应用机解除 LUKS 依赖：准备结果与切换方案

状态：2026-11-11 用户已同意准备工具、隔离演练、预复制测量与主机来源规则的建议。实际存储切换未执行；
停止网关和 Docker 的具体维护窗，须在给出实测耗时与回退证据后确认。整机重启和销毁旧加密材料另行授权。
核验与实现基线：`origin/main` / `e4849f543`。
现网身份由 [舰队入口](../../deploy/hetzner/README.md) 拥有。

## 已核实的现状

2026-11-11 07:40–07:43 UTC（北京时间 15:40–15:43），prod 只读探针成功：

- 数据库与 Redis 均由本站数据机承接，活动网关的两个 Host 都指向该数据机；应用机没有旧服务端。
- 同一 LUKS mapper 双挂到 `/var/lib/tokenkey` 和 `/srv/tokenkey-rehearsal`。
  Docker Root 为后者的 `docker` 子目录，驱动 overlay2。
- 加密文件系统已用 13,871,288,320 bytes（约 12.92 GiB）；普通 ext4 根盘可用
  105,855,176,704 bytes（约 98.58 GiB）。有空间保留旧卷并复制一份现有内容，无须购买新机。
  在线盘点存在持续写入，以上是容量估计，不是复制一致性证明。
- 在服容器：活动色、socat 控制代理、Caddy、PG/Redis 客户端；备用色处于 exited，切换不得把它误启动。
  Caddy 配置、证书状态、QA raw、capture ledger 和客户端凭据都有加密卷上的 bind source。
- `.env` 为 root:root 0600，应用目录与 capture ledger 为 UID/GID 1000、0700；复制须保留权限。
- Docker 有 mountpoint 检查、LUKS unlock 的 Requires/After、默认强制 LUKS 的 runtime 检查三类保护。
  Docker 和 tokenkey 两个服务均安装了 `tokenkey-volume.conf`。
- `/etc/fstab` 有两个 LUKS 挂载项，`/etc/crypttab` 保留该卷；unlock 服务 enabled。
  本轮公网 `/health` 返回 ok。

线上探针回执 ID：盘点 `5d9bcdcc-e74b-4f3c-a1c4-419773cf9187`，
启动依赖 `e940b92a-6202-4198-b2cd-886c84423a02`。

## 推荐终态

应用、QA、证书状态和 Docker 数据落在普通根盘，开机不再需要取得 LUKS 密钥。
`/var/lib/tokenkey` 是普通根盘目录；`/srv/tokenkey-rehearsal` 作为指向同一目录的持久 bind mount，
保留现有 Docker Root 与容器挂载路径，避免改动 Docker 保存的绝对路径和命名卷元数据。
这条兼容路径属于正式终态，不排第二次改名维护窗。

新的开机保护必须确认：普通 ext4 根盘身份符合预期、两个路径内容来源一致、兼容 bind 确实挂载、
`.env` 和 Docker 数据标记存在。失配时拒绝启动，防止 Docker 在空目录创建一个新运行环境。
不能通过删除所有 guard 或简单把旧 LUKS 检查改成永远成功来迁移。
迁出后敏感配置不再有 LUKS 静态加密保护；保留 root-only 文件权限与既有异机恢复材料，不把密钥写进文档。

## 实施前的代码与演练要求

以下为切换前验收要求；本日实际完成状态见文末，未完成项不由单元测试推导通过：

1. 为 `ops/stage0/bluegreen_host_runtime.py` 增加由站点显式选择的普通根盘身份检查，
   不改变其它平台的默认保护。反向测试错误来源、空目录、缺失兼容 bind、指向 LUKS 或其它设备均拒绝。
2. 同步 `resolve_prod_ssm_target.py`、站点矩阵，以及 `deploy_via_ssm_bluegreen.sh` 的运行参数和
   systemd guard 渲染；后续正常发版必须持续生成新保护，不能重新写回默认 LUKS 检查。
3. 准备带主机/旧挂载身份/容量/配置摘要守卫的执行器；默认只计划，分开 pre-copy、cutover、verify，
   持久保存阶段和配置备份。失败不能静默继续，也不能自动删除旧卷。
4. 用隔离目录和临时 Docker daemon 演练预复制、停写差量同步、容器身份/命名卷保留、
   guard 安装和配置回退；不挂活库、不复用活 QA 写目录或证书目录。
5. 测得预复制耗时、预估最终差量，约定长流排空上限及中断预算，再确定维护窗口。
   当前只有容量盘点，不能从容量直接承诺秒级或零中断。

## 单次维护窗

1. 在线预复制到普通根盘暂存目录，使用保留 numeric UID/GID、权限、ACL、xattr、hardlink、sparse 的复制方式。
   在线副本不具备一致性，不可直接启动。保存原配置和在服容器清单，验证回退路径与异机钥匙可用。
2. 暂停 QA provision/cleanup/archive timer，等待运行中的归档结束并取得同一生命周期锁；
   串行化发布和维护，记录 timer 的原状态。暂停看门狗需使用已有维护机制并保证恢复。
3. 按约定排空/停止活动网关，确认 capture ledger Drain 成功，再停止其它在服容器、Docker 与 socket；
   确认无容器写者或 overlay 挂载后做最终差量同步与校验。备用色保持 stopped。
4. 卸载双挂路径，把已核验的普通目录落到 `/var/lib/tokenkey`，安装兼容 bind；
   先安装新身份检查，再切换 fstab、Docker/tokenkey 的 guard、旧 unlock 依赖和站点身份配置。
   保留原 daemon 的 Docker Root 字符串及其它配置。
5. 只启动原在服集合，验证数据机 PG/Redis 连接、Caddy TLS/四域名、真实网关请求与计费、
   QA 捕获/ledger/provision/cleanup/archive、客户端探针和发版前置检查。
   恢复原 timer 与看门狗，确认旧 mapper 没有活动进程或挂载依赖。
6. 检查 Docker/tokenkey 启动图不再引用 unlock，普通路径挂载保护有效。
   不把整机重启绑进本次切换；无人值守重启验收另列出明确执行步骤和授权。

## 回退边界

- 恢复流量前失败：停止新副本，恢复原挂载、配置、依赖与原在服集合。
- 新副本接流量后，数据机已经继续接写；不得回退数据库或启动旧本地数据库。
  QA raw、ledger、证书与其它本地状态也可能已推进，不能直接丢弃新副本换回旧拷贝。
  必须先停写并核账最终差量，确定是前向修复还是将新本地状态同步回旧卷后恢复。
- 旧加密卷、异机密钥、旧配置与回执保留作回退材料。移除开机依赖与销毁旧材料是两个不同动作；
  后者不从迁移成功自动推导授权。

## 本轮同时确认的两个问题

QA capture ledger 不属于 raw 的 24 小时清理或历史 S3 的七天生命周期。
`captureledger.loadState` 读取全部 failure 文件，`evaluateHealth` 对所有 unresolved 失败计数，
没有年龄过滤或自动清理路径。`Recover` 只按单个 capture identity 定位 persist failure，
生产 capture service 也未调用它；它不能自动核销 runtime discontinuity。
prod 三条位于 11 月 8 / 9 日；cn-prod 十一条中，10 月 25 日的记录到 11 月 11 日仍在。
因此没有自然消失日期。后续应按证据恢复，或设计保留真实缺口、将历史完整性与当前运行健康分开展示的结案机制；
不能把未知缺口直接改成 recovered。

主机层来源规则，是数据机自己再检查一次「谁能连 PG 5432 / Redis 6379」。
两站 07:43 UTC 探针确认端口只绑定私网，DOCKER-USER 链为空；云防火墙此前已核验为仅放本站应用来源。
云防火墙提供当前访问保护，主机层白名单提供云规则误改时的第二层保护。
这是 07:43 UTC 的历史核查；本日后续已安装并提交两站来源规则，见文末。


## 2026-11-11 准备实现

- `bluegreen_host_runtime.py` 的 `tencent-root-bind` 模式固定校验普通根盘 UUID、非 mapper 设备、
  `/var/lib/tokenkey` 的根盘来源、兼容 bind 的 FSROOT 与目录 inode、私有 `.env` 和已有运行状态。
  `volume_contract` 由 resolver 和发版 renderer 共用；发版传递和重装 guard 使用同一份编码契约。
  当前站点矩阵仍是 `tencent-luks`，没有提前宣称切换成功。
- `prepare_app_luks_exit.py` 默认 plan；`precopy` 与 `delta` 固定检查本机身份、旧卷 UUID、根盘 UUID、
  配置摘要和容量余量，只写根盘私有 `/var/lib/tokenkey-luks-exit`。在线复制限速 30 MiB/s，
  rc24 只标记为在线不一致，不能拿来启动。`delta` 用内容校验和测量剩余差量。
- 隔离 Linux 元数据演练验证权限、UID/GID、ACL、xattr、hardlink、symlink、sparse、增量删除、
  同秒等长内容变化和反向同步。最终停写同步与回退同步必须 `--checksum`，不能仅依赖 size/mtime。
- 根盘 UUID 实测为 `9842d3d6-a839-4127-bda7-f19137effe71`；旧 ext4 UUID 为
  `12311aff-e0fa-4345-8d1f-5b9f990dfc84`。UUID 是设备标识，不是解密材料。
- 来源规则 owner 为 `ops/stage0/dataplane_source_acl.py`，按本站应用私网地址放行数据服务。
  DOCKER-USER 使用 conntrack 的原始目标地址/端口、ORIGINAL 方向匹配 DNAT；INPUT 覆盖 host/proxy 路径。
  只管理自己的两条 jump 和两条链，不恢复整张表。stage 先装五分钟自动回滚；连接验证后 commit，
  持久 unit 在 Docker 启动/重启后重装。隔离真实 TCP 演练验证两站的允许、拒绝、其它端口、重复应用及回滚。

实际执行证据见下；单元测试通过不代表应用存储已切换。


## 09:36 UTC 实测结果与待确认窗口

两站来源规则已 committed、active、enabled；应用侧 SQL、Redis PING 与公网 health 均通过。
prod 持久化服务曾因父子进程争用同一锁启动失败，已修正并完成现场服务重启，cn-prod 用修复版本首次提交通过。
未重启 Docker、数据库或应用。两站当前 QA runtime 的 pending/inflight/failed 均为 0，历史 unresolved 仍为 3/11。

预复制传输 12,811,464,329 bytes，耗时 425.939 秒；内容校验和 dry-run 扫描耗时 213.826 秒，
待同步文件总量 644,137,981 bytes。它是持续写入源上的测量，最终差量会随维护时间变化，不能视为一致副本。
隔离真实 overlay2 daemon 搬移/重启、容器 ID 与匿名卷/命名卷/绑定路径保留、备用色不启动均通过。
实际演练脚本在 `ops/stage0/rehearse_app_storage_daemon.sh`；仅使用合成 fixture 与独立 Docker volume。
锁修复后完整 preflight PASS（257 秒）；存储/发版/来源规则专项 72 项通过；此前收尾专项 37 项复跑通过。

建议申请 **单次 20 分钟的 prod 应用维护预算**：长流排空最多 10 分钟；其余预算用于停止写者、
最终 checksum 同步、换挂载/安装 guard、启动原在服集合及验证；实际完全不可用阶段预计 5–8 分钟，
这是基于 214 秒在线扫描加同步/启动余量的估计，不是生产停写演练实测。超过预算即按回退边界处理。
最近 15 分钟有 495 条已落账请求，约 33 条/分钟；按这个速率，完全不可用 5–8 分钟可能影响约 165–264 条请求，
若排空期间也拒绝新请求，整个窗口可能影响约 660 条。用量行不是入口请求总数，真实影响可能更大；不能承诺无损。

可优先考虑 **2026-11-11 22:00–22:20 北京时间（14:00–14:20 UTC）**。
过去一天对应完整 14:00 UTC 小时为 1,407 条用量，低于当前速率，仅是选择低谷的参考，不保证今晚流量相同。
窗口开始前必须再次预复制、测差量与流量；文件/配置/活动色变化重新核验，禁止使用过期的原在服清单。
没有获得该具体窗口的停流确认前不发送 SIGUSR1、不停止网关或 Docker，也不自动安排未来生产操作。

窗口中的具体落点与回退材料：

- 在根盘私有目录备份 fstab/crypttab、两个服务的 drop-ins、runtime helper 和单位启用状态，
  保存完整 Docker ID/挂载与原 running 集合，以及当次维护开始后的新 QA runtime 清单。
- 持有蓝绿发布锁，暂停并等待 QA 三项任务结束，再取 QA lifecycle 的同一把锁。
  排空复用蓝绿 `drain_container` 的健康/USR1/`health/inflight` 判定；停止后逐项检查本次 runtime 的 `drained=true`、pending/inflight=0，
  不要求历史 runtime 全部 drained。确认无 overlay 或进程占用后才做最终 checksum 同步。
- 卸载旧双挂；保留底层原目录到私有工作目录，不覆盖未知底层文件。
  同一根盘内把 staging 移到 `/var/lib/tokenkey`，持久 bind 到 `/srv/tokenkey-rehearsal`。
  Docker `data-root` 字符串不变；fstab 移除旧双挂并写入新 bind；移除旧 20/40 LUKS drop-ins，保留 30 恢复出口 guard。
- 两个 `tokenkey-volume.conf` 均安装显式 `tencent-root-bind`、固定根盘 UUID 的同一契约，安装新版 helper；
  resolver/matrix 同窗切换 `volume_platform` 并新增 `volume_root_uuid`，保留历史路径字段。
  先检查新 guard，再 daemon-reload；检查 Docker/tokenkey 不再依赖 unlock，禁用其开机启用状态。
- 启动 Docker 后，只启动维护前 running 的容器 ID；不通过 `tokenkey.service` 重建容器或误起备用色。
  接流前验证客户端连库、容器挂载/ID/状态、QA runtime 和 TLS；接流后验证实际网关请求/计量、QA 捕获/生命周期和四个域名。
  还原原 timer/观察器状态，保存持久验收回执。保持旧卷、mapper 和密钥可回退，不执行整机重启。

来源规则与存储准备的 SSM CommandId、回执摘要见
[收尾机器可读报告](reports/data-plane-closeout-20261111.json)。
