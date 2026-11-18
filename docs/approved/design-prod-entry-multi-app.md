---
title: 美西 prod 入口机 + 双应用机，旧应用机脱离 LUKS
status: approved
approved_by: "feng（2026-11-15 会话：批准设计）"
approved_at: 2026-11-15
risk: high
authors: [agent]
created_at: 2026-11-15
revised: 2026-11-15（复审修复后用户批准；恢复 §3.5/§3.6；显式废止并行替换文档 HA 入口验收；补证书离机备份契约）
---

# 美西 prod 入口机 + 双应用机，旧应用机脱离 LUKS

本文是**已批准**设计基线。设计 PR 本身不购买资源，不改防火墙、容器、DNS、证书或线上流量。
批准本设计 **≠** 批准购买或切流：阶段 A（候选机升版/看门狗/sshd，不接流）可在合并后执行；购买入口机与切 DNS（阶段 B）及之后各阶段仍须逐步会话确认。

相对 [prod 应用机并行替换](design-prod-app-parallel-replacement.md)（#3155）：

- **接管**：入口选型、预算、切流与发版形态（本文）。
- **显式废止**该文档「两应用节点前需要独立高可用入口；双 A / 单机 Caddy 不作 HA 验收」及「不把单 Caddy 当等价 HA」——用户 2026-11-15 确认单入口机、无热备，入口故障靠重建。
- **仍有效、不删除**：不中断请求、验证失败不下线旧机、不可逆操作另行审批、双应用机保留、QA 集中管理与 24h 丢失、候选准备证据。旧文档继续被舰队/QA/US 引用，本设计不归档也不删除它。

参照 cn-prod 2026-11-15 的切换与滚动发版
（[domestic README](../../deploy/domestic/README.md#入口机与多应用机2026-11-15-起)、
[cn-prod 滚动发版](design-cn-prod-rolling-deploy.md)），
不照抄其已知缺口：看门狗未测入口域名、app-b QA 清理延后、过渡期残留蓝绿容器等——prod 从一开始按终态写。

## 用户决定

| 项 | 决定 | 确认 |
|---|---|---|
| 旧应用机续费 | 已改为 `NOTIFY_AND_AUTO_RENEW`，11-21 自动续 | 已 |
| 应用机终态 | 保留 4C16G 旧机并让它脱离 LUKS；另有 1 台 2C8G 不加密新应用机。不走 3×2C8G | 已 |
| 新 2C8G 身份 | 就是现有候选机 `lhins-2kf31667`，不另买应用机 | 2026-11-15 确认 |
| 入口 | 锐驰 2C4G 单入口机；**不买 CLB、不用 Cloudflare LB** | 已 |
| 热备入口 / HA | **同 cn-prod：不需要热备入口。** 废止并行替换文档的「独立高可用入口」验收。切流稳定后关闭旧应用机公网 80/443；入口故障靠重建入口机（仅从离机密文证书备份恢复，§2.1）。不做 DNS 改回旧机，不维护旧机证书回推 | 2026-11-15 确认 |
| 切流 | 逐域名切 DNS，`api-cn.tokenkey.dev` 先切；每步可改回旧机 IP（TTL 60） | 已 |
| 发版 | **多应用机滚动，不留节点内蓝绿**；与 cn-prod 共用 `remote_exec` / `rolling` 实现，prod 先迁单容器布局再参数化路径；远程 TAT 为主、SSM 兜底 | 2026-11-15 确认 |
| Caddyfile | 入口机用模板与渲染器生成，目录挂载（不用单文件挂载） | 已 |
| 媒体存储 | 不是阻断项（§1.3） | 已 |
| QA >24h | 脱离 LUKS 时**不复制**超过 24h 的 `qa_blobs` 小时目录，按已批准规则接受丢失 | 2026-11-15 确认 |
| usw-eval1..4 | 2026-11-15 07:40 UTC 已隔离退还（§1.5） | 已 |
| 安全推进 | 批准前零线上变更；批准后阶段 A 只碰候选机（不接流）；DNS 切流前每步用 `--resolve` 验收；失败即停、可回退 | 2026-11-15 要求 |

## 1. 现状实测

### 1.0 复审补丁（相对 15:20–15:45 CST 初稿）

| 项 | 初稿 | 复审 |
|---|---|---|
| 锐驰配额 | 4/10 可用 | **20/20 可用**（以执行前再查一次为准） |
| 候选机版本 | 落后 prod | `lhins-2kf31667` 停在 **1.8.371**；prod 旧机当时 **1.8.376**。滚动前置要求 **≥1.8.377**（启动即 drain + SIGUSR2） |
| 候选机看门狗 | — | 只有蓝绿与 socat，**未装 https-watch**；sshd 密码/root 登录仍开 |
| 旧机负载 | — | 网关 CPU ~0.75/4 核、在途 ~96、RSS ~1.6GiB（上限 8g）、约 4 rps；~95% 请求在 `api.tokenkey.dev` |
| 2C8G 内存档 | 限 3g（节点内蓝绿双容器） | **改滚动单容器后可给 ~5g**；「升内存档不可行」不再成立 |
| 多节点共享状态 | 仅理论 | cn-prod 两台应用机共享 PG/Redis 已承接真实流量（入口 5xx=0），降低此项风险，**不能替代** prod 专项检查 |
| 发版 | §5 曾建议保留节点内蓝绿 | **否决**；与用户「应用机滚动、不蓝绿」一致 |

初测证据：`run-probe.sh --target prod` / `prod-data`、候选机 SSM、tccli Lighthouse/TAT、公共 DNS。
调研期间数据机曾留只读 `/tmp/q.sql`，已删除。复审为只读，未改线上。

### 1.1 主机（美西 na-siliconvalley-2）

| 角色 | 实例 | 规格 | 公网 | 私网 | SSM | TAT | 续费 |
|---|---|---|---|---|---|---|---|
| 旧应用机（接全部流量） | `lhins-fcwpwymt` | 4C16G / 180G（355） | 43.162.83.233 | 10.4.0.16 | `mi-080ed76dc6d97d8b5` | Online 1.2.2 | 自动，11-21 |
| 新应用机（候选，未接流） | `lhins-2kf31667` | 2C8G / 80G（95） | 43.162.126.44 | 10.4.0.9 | `mi-000c0168439c243a8` | Online 1.2.2 | 自动，12-11 |
| 数据机 | `lhins-p43i2kxh` | 2C8G / 100G（90） | 43.153.73.120 | 10.4.0.3 | `mi-044ba9da6453c1557` | Online 1.2.2 | 自动，12-09 |
| 遗留（关机，已过期） | `lhins-rbjrqukb` | 2C8G / 150G | 49.51.232.148 | 10.4.0.17 | — | — | 11-09 过期 |

### 1.2 旧应用机 LUKS 与承载内容

- 载体：根盘文件 `/var/lib/tokenkey-validation/recovery-rebuild-20261108.luks`（64 GiB）→ `/dev/loop0` → LUKS2 → mapper `tokenkey_recovery`（ext4，63G，已用 21G），挂在 `/var/lib/tokenkey` 与 `/srv/tokenkey-rehearsal`。
- 依赖链：`tokenkey-luks-unlock.service`（COS 取钥，`Before=docker.service`）；fstab / crypttab `noauto,nofail`；`daemon.json` `data-root=/srv/tokenkey-rehearsal/docker`；若干 docker drop-in 与 `tokenkey.service` 卷契约。主机 7 天未重启，无人值守重启从未验收。
- 根盘：`/dev/vda2` 190.1 GB，已用 89.6 GB，可用 92.7 GB（其中 65G 是 LUKS 载体）。根盘 `/var/lib/docker` 几乎未用。挂载点下被遮住的根盘目录内容**未核实**。
- 载体主要内容（2026-11-15 只读 `du`；媒体 png/jpg/mp4/webp = 0）：

| 路径 | 大小 | 迁移 |
|---|---|---|
| `app/qa_blobs/` | ~12.4G（近两日小时目录） | 只复制 ≤24h；更早接受丢失 |
| `app/logs/` | ~165M | 复制 |
| `app/qa_capture_ledger/` | ~820K | 复制（协调器需要） |
| `app/model_pricing.json`、`plugins/`、`pages/`、`qa_dlq/` | ~360K | 复制 |
| `docker/` | ~7G | **不复制**；新 data-root 重拉 |
| `qa-node1/` | ~1.6G（含 `.push-*` 暂存） | 复制；暂存可否弃由 QA 交接确认 |
| `caddy/` | ~624K（证书 notAfter **2027-02-06**） | 复制到根盘；另做离机密文备份供入口机（§2.1） |
| `.env` | 34 键 | 复制，root 0600 |
| `dataplane-client/`、`https-watch/`、蓝绿状态文件等 | 各 ≤16K | 复制 |
| `postgres-backup/`、`pgbackrest-conf/` 等遗留 | ≤12K | 复制保留，本次不清理 |

### 1.3 媒体存储不是阻断项

线上 `MEDIA_STORAGE_DRIVER=local` 且 bucket 空 → `media_store.disabled`，inline base64。多节点无本机媒体读写问题。

### 1.4 新应用机与数据机

- 新应用机：明文根盘；活动色 blue **1.8.371**；mem 3g / `GOMEMLIMIT=2GiB`；云防火墙只开 80/443；sshd 未收紧；无 `https-watch`。
- 数据机：PG `max_connections=200`；ACL 与云防火墙已放行 `10.4.0.16` 与 `10.4.0.9` 的 5432/6379。
- 旧应用机：green **1.8.376**（复审时），mem 8g，DB open 50，`REDIS_POOL_SIZE=1024`，socat `tokenkey`→活动色；`tokenkey-qa-archive.service` 曾 failed（需在 LUKS 窗口前处理或接受 QA 缺口）。

### 1.5 出口与 DNS

- usw-eval1..4 已退役；加机名单不再包含。
- 国内阿里 SOCKS `ALLOWED_SOURCES` 已含两台 prod 应用机公网 IP。阿里安全组是否一致**未核实**。Oregon 出口 owner **未核实**。
- DNS 在 Porkbun；五域名 A = `43.162.83.233`，**TTL 60**。
- CI OIDC 是否含候选 `mi-*`：**未核实**。

### 1.6 价格（2026-11-15 询价，月付 CNY）

| 项 | 月价 | RequestId |
|---|---|---|
| 锐驰 2C4G / 60G（入口机） | 60 | `15978643-4ee3-4dda-872e-081c07400372` |
| 锐驰 2C8G / 80G | 95 | `939b2cd1-b5af-425a-90ef-a2dd7e33a2f6` |

终态月费（应用+入口）：355 + 95 + 60 = **510**（不含数据机与出口）。

## 2. 目标架构

```
客户端 ──DNS(5 域名, TTL 60)──▶ 入口机 2C4G Caddy :443
                                    ├──▶ 旧应用机 4C16G 10.4.0.16:8080（明文根盘，权重 2）
                                    └──▶ 新应用机 2C8G  10.4.0.9:8080 （明文根盘，权重 1）
两台应用机 ──▶ 数据机 10.4.0.3 PG/Redis
两台应用机 ──公网 IP──▶ cn-ip5..12 / Oregon SOCKS

应用机公网 80/443：切流稳定后关闭（同 cn-prod）
入口故障：重建入口机；不做旧机热备入口 / 证书回推
```

### 2.1 入口机

- 锐驰 2C4G（60 元/月），只跑 Caddy。新实例删默认公网 22，只开 80/443；注册 SSM Hybrid；确认 TAT Online；装 `https-watch`、`disk-metrics`、BBR、`lock-down-sshd.sh`。
- 外部探测覆盖 5 域名与证书剩余天数（不可只装在入口机自己身上）。可放在旧应用机 `https-watch`，改测入口域名（须批准重装）。
- **单点**：入口机故障时五域名不可用。恢复 = 同配置重建入口机 + **仅从离机密文证书备份**恢复（分钟级）。用户接受该 HA 边界，**不**保留旧机公网入口热备。
- 证书：TLS-ALPN-01。
  - **离机备份契约（无热备后的唯一恢复材料）**：切任一正式 DNS 前，把五域名证书与 `data/legal` 打成密文包离机存放（控制面/操作员 vault，路径实现期定，本设计锁定「必须有且可取回」）；入口机每次成功续期后更新同一备份；重建入口机只从该备份恢复，不依赖旧应用机公网 80/443。
  - 切 DNS 前另将同一份材料装入入口机；切后由入口机自行续期。旧机不再对外终止 TLS，无需证书回推旧机。
- Caddyfile **目录挂载**（cn-prod 踩过单文件挂载 `sed -i` 换 inode 读旧内容）。

### 2.2 多域名迁到入口机

站点：`tokenkey.dev`、`callmodel.io`、`api.tokenkey.dev`、`api.callmodel.io`、`api-cn.tokenkey.dev`（以线上 Caddyfile 为准）。

- 复用 `deploy/stage0/Caddyfile` 与渲染器；增加「后端列表 + 权重」：上游改为两台应用机私网 8080，`lb_policy weighted_round_robin 2 1`，`health_interval 3s`，`health_fails 2`；保留 `lb_try_duration 30s` 等。
- 只用主动健康检查。`PROD_EDGE_REQUIRED_ENV` 与 `import tokenkey_api_machine` 门禁对入口机生效。
- 客户端 IP：入口机写 `X-Forwarded-For`/`X-Real-IP`，删 `CF-Connecting-IP`。应用机 8080 只放行入口机私网 /32。经 socat 后网关记录的 IP **未核实**，切流前用测试请求实测。

### 2.3 应用机私网入口

每台应用机：单容器 `tokenkey` 监听 `<private_ip>:8080`（与 cn-prod 终态一致）。
**2026-11-17 M6**：滚动已就绪，节点内 socat/蓝绿发版路径退役（与 cn-prod M6 对齐）；
`deploy_via_ssm_bluegreen.sh` / `legacy-deploy` 默认失败关闭，仅
`STAGE0_ALLOW_LEGACY_BLUEGREEN=1` 可应急。
不往应用机 Caddyfile 加私网站点。

### 2.4 应用机规格与连接预算

| 节点 | mem / GOMEMLIMIT | DB open | Redis pool | 权重 |
|---|---|---|---|---|
| 旧 4C16G | 8g / 6GiB（可保持） | 50 | 建议脱离 LUKS 后降到 96–256（待定） | 2 |
| 新 2C8G | **~5g / 对应 GOMEMLIMIT**（单容器；矩阵显式给出，缺省或 8g 拒绝） | 25 | 96 | 1 |

PG：稳态约 75；滚动时一台摘流，无双色重叠。旧机脱离 LUKS 窗口由 2C8G 单独承载，须先过 §3.2 门禁。

### 2.5 多节点共享状态门禁

接流前实测：OAuth/登录 session、凭据刷新锁、认证缓存失效、并发/RPM、调度租约、后台单例。
`model_pricing.json` / `plugins/` 是否仅本机缓存**未核实**。媒体已排除。

## 3. 旧应用机脱离 LUKS

### 3.1 选址：根盘，不买云硬盘

需落盘约 ≤22G（含新 Docker Root 重拉），根盘可用足够。路径与新机一致：`/var/lib/tokenkey`、`/var/lib/docker`；卷契约 `tencent-root` + 根盘 UUID + SSM 身份 + hostname。

### 3.2 前置条件（旧机不接流时才迁）

1. 入口机上线，五域名 DNS 均指向入口机，稳定 **≥24h**。
2. 新 2C8G 与旧机**同一 tag（≥1.8.377）**，已接真实流量；§2.5 已过。
3. **2C8G 单台承载门禁**：旧机权重 0 后单独扛 ≥1 个业务高峰；CPU、RSS、`in_flight`、PG、平台错误率无异常。不满足则另开窗口或临时加机，不硬迁。
4. 旧机直连业务流量 0（公网 80/443 已关或访问日志无业务）≥1h；入口机无请求打到 10.4.0.16。
5. 旧机在途 0，异步 usage 已落库。
6. QA：`tokenkey-qa-archive` 已恢复或明确接受窗口缺口；暂停相关 timer；**>24h blobs 不复制**。
7. 冻结旧机 `.env` / Caddyfile；窗口内不做其它发版。

### 3.3 步骤（摘要）

| 步 | 动作 | 流量 | 可逆 |
|---|---|---|---|
| L1 | 入口权重旧机→0；观察 §3.2.3–5 | 2C8G 承载 | 是 |
| L2 | 在线 rsync 到根盘暂存（排除 `docker/`；`qa_blobs` 仅 ≤24h） | 无 | 是 |
| L3 | 停 QA timer；drain 停服务；停 docker；末次 rsync | 无 | 是 |
| L4 | 备份系统文件；改 fstab/crypttab/daemon.json/drop-in/卷契约 | 无 | 是 |
| L5 | umount；暂存改名就位；close mapper / loop | 无 | 是 |
| L6 | **重启验收**（已摘流） | 无 | 是 |
| L7 | 按滚动布局部署同 tag；loopback + 入口私网 Host 头烟测；真实授权核对 usage | 无 | 是 |
| L8 | 恢复 QA；权重 0→1→2 | 旧机回流 | 是 |

（无 L9：不恢复旧机热备 Caddy。）

### 3.4 回退

- **L1–L5 失败且旧机未重新接流**：停 docker；从 `pre-plain-<ts>/` 恢复系统文件；`systemctl enable --now tokenkey-luks-unlock.service`（COS 取钥、开 mapper、挂载）；启动 docker 与 `tokenkey.service`。载体上的旧 Docker Root/容器仍在。2C8G 继续承载。COS 取钥失败时按 [unlock contract](../ops/tencent-unlock-contract.md) 用操作员 vault 手动解锁。
- **L6 起不来**：TAT/SSM 按上条恢复；仍不行用 Lighthouse VNC。
- **L8 后出问题**：入口权重回 0；L8 之后明文盘新写入回退会丢，需记录。
- 不碰数据机。

### 3.5 旧 LUKS 载体与异机密钥的退役条件

全部满足才进入 §3.6：

1. 旧机在明文根盘上接流 ≥7 天，期间至少一次 L6 之外的重启或发版，未触发回退。
2. 载体无仍需数据：L2/L3 清单与校验和核对通过；载体独有的 `qa_blobs` 小时均已过 24h；`qa-node1/.push-*` 已由 QA 交接确认；`.env` 在根盘，证书在入口机与离机备份各有可用副本。
3. 无进程持有载体：`losetup -a` 空、mapper 不存在、`tokenkey-luks-unlock.service` disabled。
4. 无其它副本：删前再查 `DescribeSnapshots`/`DescribeBlueprints`；无导出副本。
5. 用户单独批准。

### 3.6 不可逆步骤（各自单独批准，按此顺序）

| 序 | 动作 | 前置 |
|---|---|---|
| X1 | 删除 `/var/lib/tokenkey-validation/recovery-rebuild-20261108.luks`（可先 `cryptsetup luksErase` 再删文件） | §3.5 全部满足 |
| X2 | 删除 COS 自动解锁密钥对象并收回取钥权限（对象位置**未核实**，先查清） | X1 完成 |
| X3 | 删除操作员 vault：`~/.local/state/tokenkey-luks-offhost-vault/recovery-luks.key.enc` 与 `~/.local/share/tokenkey-custody/luks-wrap-20261022.key` | X1 完成 |
| X4 | 删除 `tokenkey-luks-unlock` unit/脚本、`pre-luks-underlay`、`pre-plain-<ts>/`、旧暂存 | X1 完成且确认不再回退 |

先删密钥会在载体仍在时造成永久丢数；载体销毁后再删密钥。X1 之后更新舰队文档、unlock contract、data-plane-split 相关节与 `candidate-usw-20261022.json` 的 `volume_platform`（另开文档 PR）。

## 4. 切流与回退（用户无感）

### 4.1 准备（不碰正式流量）

1. **阶段 A**：候选机升到当前 prod tag（≥1.8.377）；`lock-down-sshd.sh`；装 `https-watch`；核对 SSM/TAT/CI `mi-*`。
2. **阶段 B 购买入口机**（须批准）：§2.1 初始化；两台应用机放开入口机→8080 /32；渲染 Caddyfile；密文复制证书。
3. `--resolve` 验五域名 TLS/网页/`@machine`/301；真实授权 usage；长 SSE 与大上传；drain 摘流行为。
4. 任一失败：不改 DNS。

### 4.2 逐域名切 DNS

顺序：`api-cn.tokenkey.dev` → `api.callmodel.io` → `callmodel.io` → `api.tokenkey.dev` → `tokenkey.dev`。
每步观察 15–30min：入口 5xx、后端分布、usage、平台错误。
回退：A 记录改回 `43.162.83.233`（**仅在旧机仍开公网 80/443 且 Caddy 仍服务时有效**）。
五域名稳定后：关闭旧机公网 80/443（同 cn-prod）；旧机 Caddy 可停。此后入口故障**不再**走 DNS 回旧机，走重建入口机。

### 4.3 旧机脱离 LUKS

DNS 全切且稳定 ≥24h，且 2C8G 单台门禁通过后，按 §3。可与阶段 C 代码工作并行准备，**不可**与切流并行执行破坏性步骤。

### 4.4 加机名单（两站共用习惯）

数据机 ACL/防火墙、出口 `ALLOWED_SOURCES`、入口后端权重、应用机 8080、PG 预算、CI 权限、节点矩阵、QA source id、SSM/TAT 身份——加减机逐项同步。建议只读校验脚本（读矩阵比对线上），实现期另 PR。

## 5. 发版：多应用机滚动（与 cn-prod 共用，prod 先对齐布局）

1. **节点矩阵**：每节点 `lhins-*`、`mi-*`、TAT/SSM region、私网、hostname、卷契约、内存档、权重、QA source id。执行前双通道身份核对。
2. **布局先行（最大代码块）**：prod 今日是 `/var/lib/tokenkey` + 长脚本生成的 `docker-compose.bluegreen.yml`；cn-prod `rolling.py` 写死 `/opt/tokenkey`、单 `tokenkey` 容器、`compose.node.yml`。
   **必须先把 prod 迁到单容器布局，再把 rolling 的布局参数化**；不能把 cn-prod 脚本直接打到现网 prod。
3. **内存档**：矩阵显式给出；2C8G 缺省或 8g → 拒绝。
4. **逐节点**：确认它机健康 → drain → 换镜像 → loopback 烟测 → 私网 Host 头检查 → 入口回流 → 下一台。失败停，不自动回滚已完成节点。公网 canary 不能代替逐节点检查。
5. **不留节点内蓝绿**。双节点时一台摘流、另一台承载；2C8G 单台门禁未过前，不把旧机从入口摘掉做破坏性维护。
6. **跨节点锁**：入口机租约或等价物；CI 与手工共用。
7. **TAT 主路径**：CAM/OIDC、命令长度、先下发文件再执行等约束实现前逐项确认；就绪前 SSM 可为实际主路径。
8. **边缘**：发版只改应用机；`sync_caddyfile` 目标改为入口机。
9. **镜像**：滚动前全节点预拉并校验 digest。
10. **版本门禁**：全节点同一 tag 才开始；候选机升版前不得接流。
11. **cn-prod 教训写入实现**：Docker 网络在 compose 外创建时声明 `external_network`；Caddyfile 目录挂载。

## 6. 建议执行顺序

| 阶段 | 内容 | 用户流量影响 |
|---|---|---|
| A | 候选机升版、收紧 sshd、装看门狗、核对 SSM/TAT/CI | 无（不接流） |
| B | 买入口机 → 渲染/证书/`--resolve` → 逐域名 DNS → 稳定后关旧机公网 80/443 | DNS 每步可回退（关防火墙前）；关防火墙后靠重建入口 |
| C | prod 单容器布局 + `rolling` 参数化（可与 B 并行写代码） | 布局切换窗口：逐台、可回退；须单独批准执行 |
| D | 旧机脱离 LUKS | 窗口内仅 2C8G 为后端；入口仍在 |

**批准本设计 ≠ 批准购买或切流。** 阶段 B/C/D 各执行前再确认一次。

## 7. 风险与未核实

- 入口单点；无热备入口；恢复靠重建。**对外/投标口径**：多地域（prod + cn-prod）+ 每站多应用机仍构成等效容灾与应用 HA；本条仅描述地域内入口机取舍，见 `docs/decisions/decided.md`（2026-11-18）与 `deploy/hetzner/README.md`「高可用与等效容灾」。
- LUKS 窗口内 2C8G 单独承载。
- `.env`/QA 落明文根盘（与新机同一取舍）。
- 客户端 IP 信任链配置错误风险。
- TAT CI 身份与命令长度未验证。
- 未核实：socat 后客户端 IP、`model_pricing.json` 缓存语义、LUKS 遮挡下原目录、COS 密钥位置、CI `mi-*`、Oregon/阿里出口一致性、旧机快照是否存在。

### 仍待执行期决定（不阻塞本设计批准）

1. 2C8G 单台承载门禁的具体时长与阈值。
2. L6 重启窗口排期。
3. 旧机 `REDIS_POOL_SIZE` 是否在脱离 LUKS 后那次发版下调。
4. TAT 的 CI 身份方案；就绪前是否继续 SSM 主路径。
5. 过期关机实例 `lhins-rbjrqukb` 是否放弃；旧机 16379 临时规则清理时间。

## 8. 本 PR 范围

- 仅本设计文档 + 决定台账 + 索引指针 + 并行替换文档的 HA 移交说明。
- **不**创建云资源、**不**改 DNS/防火墙/容器、**不**发版。
- `approved_by` 已写入；阶段 B/C/D 执行前仍须单独确认。
