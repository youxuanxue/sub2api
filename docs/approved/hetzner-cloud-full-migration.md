---
title: TokenKey 全线迁移 Hetzner Cloud（prod + edge · 德区 · 4c8g 过渡）
status: pending
approved_by: pending
created: 2026-10-09
revised: 2026-10-09
# revised note: cax21 lock + Jobs review §16 + gates E0/L0
owners: [tk-platform]
scope: >-
  deploy/hetzner/* + .github/workflows/deploy-*-hetzner*.yml +
  ops/hetzner/* + edge/prod 矩阵 + Stage0 bootstrap/bluegreen 控制面适配 +
  skills（expansion / IP rotation / release）+ DNS/ACME/smoke/diagnostics +
  凭证与 hcloud 自动化通路
related_designs:
  - docs/approved/deploy-stage0-workflow.md
  - docs/approved/edge-bluegreen-release-safety.md
  - docs/approved/design-fleet-pgdump-restore-canary.md
  - docs/spec-delta/edge-lightsail.md
---

# TokenKey 全线迁移 Hetzner Cloud

> **状态：`pending`（待人工审批）。** 未审批前禁止开生产实例、改 DNS 切流、或合并替换
> Lightsail/EC2 为唯一路径的代码。本文件是审批基线，不是实施日志。
>
> **本修订锁定的人工决策（2026-10-09）：**
> 1. **区域**：过渡期 **prod + 全部 edge 一律德国 `fsn1`**。
> 2. **机型**：过渡期一律 **`cax21`（4 vCPU / 8 GiB / arm）**——德区 CX 缺货；CLI 已验证可开。
> 3. **架构**：过渡舰队 **arm64**（与现 prod Graviton 一致；拉 `linux/arm64`）。
> 4. **目标（诚实版）**：计费与账号数据不丢；API **无长时间硬中断**；可在分钟级回滚。
>    **不**承诺延迟/上游成功率与今日全球多区出口完全一致。
> 5. **风险关闭**：§17 实测门禁；Edge DNS 前 E1–E4；Prod DNS 前 P1–P4/P6。

## 0. 一句话目标

把 **prod 主网关（现 EC2 `c7g.xlarge` 4c/8G arm64）+ 全部 deployable edge（现 Lightsail
`small_3_0` ≈ 2 GiB）** 迁到 **Hetzner Cloud 德国 `fsn1`**，过渡机型统一 **`cax21`
（4 核 / 8 GiB arm64）**；建立与今日 `aws` CLI / GHA OIDC / SSM 蓝绿对等的 **`hcloud`
自动化运维通路**。

## 1. Background / 为何现在做

| 现状 | 痛点 |
|---|---|
| Edge：`small_3_0`（约 **2 GiB**） | 多模态/生图峰值易 OOM（us5/us6 等） |
| Prod：`c7g.xlarge`（**4 vCPU / 8 GiB** arm64） | 全家桶同机；控制面深绑 AWS |
| 控制面：GHA OIDC → AWS IAM → SSM | 迁 IaaS 必须显式重选或薄保留控制面 |
| 区域分散：uk / us-east-2 / us-west-2 | 运维矩阵碎；本方案过渡期收敛到德区便于观察 |

**不做半吊子「只迁一台」**：目标是 **Hetzner 成为 prod+edge 唯一 IaaS**；AWS 最多保留薄控制面（§3 D1）。

## 2. 非目标

- 不在本方案内重写 gateway 业务代码、定价 / 候选资格 SSOT。
- 不把 Object Storage / 托管 PG / 托管 Redis 作为过渡期必选项（Stage0 仍单机 compose；
  prod 数据盘用 Hetzner Volume）。
- 不要求第一天关掉 AWS 账号（双跑与回滚窗口必须存在）。
- **不在过渡期追求全球低延迟出口**——美/英用户到德区 RTT 会升高；验收以「不断服、数据完整、
  错误率可回滚」优先，地理延迟在观察期另表。
- 不把 Console 账密当成自动化凭证（见 §14）。

## 3. 必须先拍板的决策（审批勾选项）

### D1 — 控制面（二选一，推荐 A）

| 选项 | 做法 | 优点 | 代价 |
|---|---|---|---|
| **A（推荐）SSM Hybrid 薄控制面** | 机器在 Hetzner；bootstrap 注册 **AWS SSM Hybrid**；`deploy_via_ssm_bluegreen.sh` / smoke / diagnostics **基本复用**；GHA 仍 OIDC→AWS | 改动面最小；蓝绿/烟测/日诊不断档 | 仍依赖一个 AWS 账号做 SSM+Secrets |
| **B 原生 Hetzner 控制面** | `hcloud` + cloud-init + **SSH/Tailscale**（或自建 runner）驱动蓝绿；Secrets 迁 GitHub Environment / Bitwarden | 无 AWS 运行时依赖 | 需重写几乎全部 `ops/stage0/*` SSM 入口 |

**人工确认（2026-10-09）：D1=A（SSM Hybrid 薄控制面）。**  
Phase-1～4 一律走 A；是否改 B（SSM 退场）仅在稳定 ≥1 个发布周期后另审批（Phase-5），不与迁机同 PR。

### D2 — CPU 架构

| 选项 | 含义 |
|---|---|
| **X 全舰队 amd64** | CX33/CPX31；德区 CX **Limited availability**，过渡期不可依赖 |
| **Y（已锁定）全舰队 arm64** | **`cax21`**；与现 prod Graviton 同拱；GHCR multi-arch，运行时拉 `linux/arm64` |

**人工确认（2026-10-09）：D2=Y（`cax21` / arm64）。**  
禁止 `simple_release` 单拱 amd64-only 打到 Hetzner 舰队；edge 从 Lightsail x86 迁入时改拉 arm 镜像。

### D3 — 迁移节奏

| 选项 | 含义 |
|---|---|
| **R1（推荐）Edge-first** | 先迁 1 台 canary edge → 全 edge → 最后 prod（数据最重） |
| **R2 Prod-first** | 先搬计费真相源；edge OOM 风险持续更久 |

**审批默认提议：R1。**

### D4 — 德国 location（二选一）

| 选项 | 含义 |
|---|---|
| **G1（推荐）`fsn1`（Falkenstein）** | 库存与机型常见；全舰队默认 location |
| **G2 `nbg1`（Nuremberg）** | 备选；与 fsn1 同属德区，延迟差异通常可忽略 |

**人工确认（2026-10-09）：G1 `fsn1`。** 过渡期 **prod 与全部 edge 同一 location**，降低矩阵复杂度；升配/拆区另审批。

### D5 — 过渡机型（4c/8G · 有货优先）

人工约束：过渡期一律 **4 vCPU / 8 GiB**、德区。报价为 **2026-10-09** `hcloud` 实拉（`price_monthly.gross`，未含 IPv4/Volume/税）。

#### 可用机型对照（德区 `fsn1`/`nbg1` 同价）

| SKU | vCPU | RAM | 本地盘 | 架构 / CPU | €/月（fsn1） | 过渡期裁决 |
|---|---:|---:|---:|---|---:|---|
| `cx33` | 4 | 8 GiB | 80 GB | x86 / shared | **€9.99** | **理想性价比，但 Cost-Optimized 常 Limited / 难订** |
| **`cax21`** | 4 | 8 GiB | 80 GB | **arm** / shared | **€12.49** | **已锁定（有货；CLI 2026-10-09 建通）** |
| `cpx31` | 4 | 8 GiB | **160 GB** | x86 / shared | €20.49 | 备选；多区亦曾报 unavailable |
| `cpx32` | 4 | 8 GiB | 160 GB | x86 / shared | €41.99 | 不选 |
| `ccx23` | 4 | **16** GiB | 160 GB | x86 / dedicated | €101.49 | Phase-5 升配（注意：升配回 x86 需换镜像拱） |

| 角色 | 过渡 SKU（锁定） | 备注 |
|---|---|---|
| Edge ×N | **`cax21` @ `fsn1`**（**人工确认 2026-10-09 晚**） | 相对今日 ~2 GiB 为 **4× RAM**；生图尖峰靠 swap + 并发闸 |
| Prod ×1 | **同 `cax21`**；数据 **Volume ≥80 GiB** | 与现 prod arm64 同拱；系统盘 80G，数据不上根盘 |

**为何改锁 `cax21`：** CX 线缺货/限额；同规格仅贵 ~€2.5；arm 与现 prod 一致，避免「迁机还要换拱」。  
**性价比：** 有货的 4c/8G 里 `cax21` 是可执行最优；`cx33` 更便宜但不可作为过渡依赖。

**观察升配触发：**

- `MemAvailable < 1.5 GiB` / OOM → 另审批升配（优先同拱更大 CAX，或接受换拱上 CCX）。
- 磁盘紧 → 先加 Volume。
- 禁止未限并发就把重生图打满 8 GiB edge。

---

## 4. 目标拓扑（过渡期）

### 4.1 区域与命名

| 逻辑 edge id（复用） | 过渡 Hetzner location | 正式域名（不变） | 说明 |
|---|---|---|---|
| uk1, uk2 | `fsn1`（或 `nbg1`） | `api-uk*.tokenkey.dev` | 出口地理：伦敦 → 德；接受延迟/风控差异 |
| us3, us4, us5, us6 | 同上 | `api-us*.tokenkey.dev` | 出口地理：美东/美西 → 德；**最大 UX 变量** |
| fra1 / sg1（若开通） | 同上 | 既有域名 | 过渡期不追求就近；sg 延迟最高 |
| **prod** | 同上 | `api.tokenkey.dev` | 与 edge 同区，简化 Volume/备份 |

主机名建议：`tokenkey-edge-<id>-hz-cax21`、`tokenkey-prod-hz-cax21`。

### 4.2 网络与磁盘

- **公网**：Primary IPv4（付费）+ 可选 IPv6。
- **边缘污染轮换**：Hetzner **Floating IP**（对齐今日 Lightsail Static IP 语义）+
  exclusion registry（新前缀，不与 Lightsail 污染表混写）。
- **数据盘（prod 必选）**：Hetzner Volume ≥ 80 GiB，挂载 `/var/lib/tokenkey`
  （PG/Redis/Caddy/secrets）。无 CFN Retain → 脚本 **fail-closed** + 标签
  `tokenkey.io/role=prod-data` + 销毁前人工确认。
- **Edge 数据**：默认本地盘即可；secrets 仍走 SSM 参数前缀
  `/tokenkey/hetzner/<id>/…`（D1=A）或密封文件（D1=B）。
- **Firewall**：入站 80/443；**公网 SSH 默认关**（D1=A 靠 SSM；D1=B 靠 Tailscale/跳板 CIDR）。
- **Swap**：edge/prod 过渡期保留 **≥2 GiB swap**（对齐今日 edge 习惯），防止尖峰直接 OOM；
  不把 swap 当容量规划依据。

### 4.3 粗算月费（基于 2026-10-09 `hcloud` 实价，未含税）

假设 deployable edge = 6 + prod = 1，全 **`cax21` @ `fsn1`**：

| 组件 | 单价 | 小计 |
|---|---|---|
| 7 × `cax21`（4c/8G/80G arm） | €12.49 | **€87.43** |
| Primary IPv4 ×7（数量级） | ~€0.50 | ~€3.5 |
| Floating IP（按需） | ~€数欧元/个 | 按启用数 |
| Prod Volume 80 GiB（数量级） | ~€数欧元 | +€数 |
| **合计（过渡，无升配）** | | **约 €95–115/月** |
| 对照若全 `cx33`（常无货） | €9.99×7 | €69.93 仅机器 |
| Phase-5 升配 | 另议 | 不默认全舰队 |

相对 `cx33` 方案约多 **€17/月** 全舰队，换的是 **可下单**。注意：账号 **Primary IPs / S.vCPUs Limits** 须预留（曾因 10/10 IP 导致 API 403）。

---

## 5. 硬约束（诚实版）

| 原则 | 做法 |
|---|---|
| **禁止双写业务库** | 任意时刻只有一个 prod PG 接受写 |
| **回滚分两档** | **写前**：DNS/镜像回旧机即可。**写后**：禁止裸 DNS 回旧库；要么留在新库，要么先 `新→旧` dump 回灌再切 |
| **Edge 双跑** | staging 域名实测 → mirror 灰度 → 正式 DNS；旧 Lightsail 停机保留 ≥7 天 |
| **Prod 切换** | 冻结写 ≤5 min → dump → restore → 关键表校验 → 计费冒烟 → DNS；EC2 停机保留 |
| **Redis（Prod）** | **不迁 Redis 数据**（接受 sticky/会话重置，TTL 本就约 1h）；切流后观察粘性 miss，不另建同步管道 |
| **Secrets 不进 git** | 仅 SSM / 密封通道 |
| **体验承诺** | 无长时间硬中断 + 可回滚 + **§17 门禁全绿**。延迟/风控用实测数字说话，不宣称「无感」 |

**风险关闭总方法：** 每个风险 = **一条现有命令实测 → 一条数字门禁 → 一条最小对策**。不新增平行平台、不写第二套监控。细节见 §17。

---

## 6. 分阶段 Workflow（端到端）

```text
Phase-0 凭证与账号（无切流）
    → Phase-1 平台骨架（仓库 + GHA + 非生产实机）
    → Phase-2 Edge canary（双跑，DNS 灰度）
    → Phase-3 全 edge 迁完 + Lightsail 退役门禁
    → Phase-4 Prod 迁入 + EC2 退役门禁
    → Phase-5 观察升配 / 控制面瘦身（另审批）
```

### Phase-0 — 凭证与账号（今天就能做，不切流）

见 **§14 凭证清单**。产出：

1. Hetzner 项目（建议 `tokenkey-prod` / `tokenkey-edge`，或单项目 + label）。
2. Read&Write API token → 仅存密码管理器 + 稍后 GitHub Environment。
3. 运维机安装 `hcloud`，`hcloud context create tokenkey`。
4. SSH 公钥注册到 Hetzner（即使默认关公网 SSH，仍用于紧急/rescue）。
5. 若 D1=A：确认 AWS 侧仍可建 **SSM Hybrid Activation**（可复用 Lightsail addon 思路）。

**验收：** `hcloud server list` 成功；**零** 付费生产机、**零** DNS 变更。

### Phase-1 — 平台骨架（无生产流量）

**仓库落地（新 owner，禁止继续往 `deploy/aws/lightsail` 塞 Hetzner）：**

```text
deploy/hetzner/
  README.md
  edge-targets-hetzner.json          # 矩阵 SSOT（location=fsn1、server_type=cax21、arch=arm）
  prod-target-hetzner.json
  resolve-edge-hetzner-target.py
  provision-edge.sh                  # hcloud server create + cloud-init
  provision-prod.sh
  render-bootstrap.sh                # 从 Stage0 源渲染；--check 进 preflight
  generated-cloud-init.yaml
  firewall-baseline.json
ops/hetzner/
  rotate-floating-ip.sh
  ensure-swap.sh
  attach-volume.sh
.github/workflows/
  deploy-edge-hetzner-stage0.yml     # provision|upgrade|rollback|smoke
  deploy-hetzner-prod.yml            # 或扩展 deploy-stage0 的 platform=hetzner
.cursor/skills/
  tokenkey-stage0-edge-hetzner-expansion/
  tokenkey-stage0-edge-hetzner-ip-rotation/
```

**矩阵硬门禁（过渡）：**

- `location`：**仅 `fsn1`**。
- `server_type`：**仅 `cax21`**（过渡）；更高档仅 Phase-5 升配 PR 放开。
- `architecture`: **arm**；镜像必须含 `linux/arm64`。
- `deployable=true` 且 `platform=hetzner` 的行必须带 Floating IP / domain / ssm_prefix。

**验收：** 非生产机 `provision → SSM online（或 ssh probe）→ compose up → /health`；
**零** Porkbun 正式域名切换。

### Phase-2 — Edge canary（双跑 + §17 实测门禁）

**canary 默认：`uk1`（德区地理变化相对小，先验证通路）→ 再 `us5`（验证跨洋出口/风控）。一次只迁一个。**

0. **L0** Limits 余量够再 provision。
1. provision + Floating IP + staging 域名 `api-<id>-hz.tokenkey.dev`。
2. **E0** 确认 arm64 镜像 → **E1–E4**；任一红 → 停，只做对策，不切正式 DNS。
3. mirror：先 **1 个** 低敏账号 → ≥2h → 该 edge 全量。
4. 正式 DNS；Lightsail **stopped ≥7 天**。
5. 60 min **E5**；超阈 → mirror+DNS 回滚。

**回滚（写前/无状态 edge）：** DNS + mirror 回 Lightsail；Hetzner 保留不删。

### Phase-3 — 全 edge 滚动

顺序：`uk1` 验证通路 → `us5` 验证跨洋风控 → 其余 uk/us → 新 edge 直接 Hetzner 出生。  
每台重复 Phase-2（含 §17 E1–E5）。上一台 E5 未绿满 60 min，不开下一台。

**Lightsail 退役门禁：**

- 无 `platform=lightsail && deployable=true`
- 污染 IP registry 冻结 Lightsail 段
- skill / workflow 标 deprecated；删除另 PR

### Phase-4 — Prod 迁移（最高风险窗口）

1. **预演（无切流）**：Volume + 同 tag 镜像 + secrets；跑 **§17.3 P1–P3**
   （restore 校验、S3 备份往返、Volume 快照）。
2. **切换日（维护窗，目标 ≤5 min 冻写）**：
   - TTL 已预先压短；公告维护窗
   - 开写冻结 → 最终 `pg_dump` → restore → **§17.3 P4 关键表门禁** → 计费冒烟
   - DNS → 解冻 → **§17.3 P5** 值守 60 min
3. 观察 24–72h；EC2 **停机保留**；OIDC TargetInstanceId → 新 `mi-*`。
4. **回滚：** 见 §5 两档；写后禁止裸回旧库。

### Phase-5 — 观察升配 / 控制面瘦身（另审批）

- 按 §3 D5 触发把个别或全舰队升到 ≥16 GiB。
- 若需恢复美东/美西就近出口，再开「德区核心 + 美区边缘」二期（**本文件过渡期不做**）。
- D1=A 稳定后另评估去掉 AWS SSM。

---

## 7. 完整运维面（Ops）对照

| 能力 | 今日（AWS） | Hetzner 目标 |
|---|---|---|
| 开通 edge | `deploy-edge-lightsail-stage0.yml` provision | `deploy-edge-hetzner-stage0.yml`（`hcloud`） |
| 升级/回滚 | SSM `deploy_via_ssm_bluegreen.sh` | **同脚本**（D1=A）或 SSH 蓝绿（D1=B） |
| 烟测 | `external_health.sh` + `edge_post_deploy_smoke.sh` | 复用；矩阵认 `platform` |
| 日诊 | `ops-daily-diagnostics.yml` | 增 `platform=hetzner` |
| IP 污染轮换 | `ops/lightsail/rotate-static-ip.sh` | `ops/hetzner/rotate-floating-ip.sh` |
| Mem/Disk 告警 | `tokenkey-disk-metrics-edge.sh` | 同脚本；阈值按 **8 GiB** 校准 |
| 发布 | `release.yml` multi-arch → deploy | 不变；**host arm64**（禁止 amd64-only tag） |
| CLI | `aws` | **`hcloud` +（D1=A 时）`aws`** |
| Skill | lightsail-expansion / ip-rotation | 平行 hetzner skills |
| 密钥 | SSM Parameter / Secrets | D1=A：仍 SSM；机上 `.env` 不进 git |

### 7.1 推荐操作者日课（骨架落地后）

```bash
# 解析
python3 deploy/hetzner/resolve-edge-hetzner-target.py --edge-id us5

# 开通 / 升级（经 GHA，勿直打生产）
bash scripts/stage0/dispatch-edge-deploy.sh --platform hetzner --edge-id us5 --operation upgrade --tag X.Y.Z

# 本地只读
hcloud server list -o columns=id,name,status,ipv4,type,location
hcloud server describe tokenkey-edge-us5-hz-cax21
hcloud floating-ip list
hcloud volume list

# 污染轮换
bash ops/hetzner/rotate-floating-ip.sh us5 --apply
```

### 7.2 禁止事项

- 禁止把 `HCLOUD_TOKEN` / Console 密码写进仓库、镜像、issue、聊天日志。
- 禁止无确认销毁带 `tokenkey.io/role=prod-data` 的 Volume。
- 禁止迁移 PR 与业务功能 PR 混意图。
- 禁止过渡期矩阵写入非德区 location（除明确废案）。
- 禁止方括号 skip-ci 标记进 VERSION/release。

---

## 8. 工作流状态机

### 8.1 单 edge

```text
prepare
  → 矩阵行 / Environment secrets / Firewall / SSM Activation
provision
  → hcloud server create (cloud-init, cax21@fsn1)
  → wait SSM mi-* (D1=A) 或 ssh probe (D1=B)
  → assign Floating IP
  → DNS staging 名
  → smoke
cutover
  → DNS 正式名 + prod mirror base_url
  → smoke full + 内存观察
upgrade / rollback
  → 现网蓝绿 owner
decommission-aws
  → 停 Lightsail / 解绑 Static IP / 清旧 mirror 指向
```

### 8.2 Prod

```text
provision + Volume
  → restore canary（非切流）
  → freeze / final dump / restore / billing smoke
  → DNS cutover
  → observe 24–72h
  → EC2 stop（保留）
  → 退役门禁另 PR
```

---

## 9. 验证与验收

### 9.1 自动化（CI / preflight）

- 矩阵 schema + resolve 单测（正/负：未知 edge、非德区 location、非 4c8g SKU 拒绝）
- bootstrap 漂移 `--check`
- 蓝绿 safety：`platform=hetzner` + 8 GiB 容量策略
- existence-only 禁令保持

### 9.2 实机门禁

| 门禁 | 标准 |
|---|---|
| `/health` | 200 + 既有 JSON |
| edge smoke full | infra + oauth + main-via-edge 绿 |
| 内存观察 | 记录尖峰 MemAvailable；OOM/mem-guard 须归因 |
| 发布 | 真实 tag upgrade + rollback 演练一次 |
| Prod 数据 | dump/restore checksum + 计费冒烟通过才允许 DNS |
| 日诊 | hetzner 行信号非空 |

### 9.3 诚实报告

未跑实机写 **未验证**；禁止用「脚本已写」冒充迁完。

---

## 10. 回滚总策略

| 阶段 | 回滚 |
|---|---|
| Phase-0/1 | 删测试机；无 DNS |
| Phase-2/3 | DNS + mirror 回 Lightsail；Hetzner 保留 |
| Phase-4 | DNS 回 EC2；Volume 快照；SSM/OIDC 指回旧实例 |
| 数据 | 以 dump/快照为真相；禁止长时间双写 |

---

## 11. 文档 / Skill / 规则同步清单（实施 PR 用）

- [ ] 本文件 `pending` → 人工改 `approved` + `approved_by`
- [ ] `docs/spec-delta/edge-lightsail.md` 标注 superseded-by
- [ ] `CLAUDE.md` / `AGENTS.md` 仅加指针
- [ ] `tokenkey-stage0-release-rollout` 支持 hetzner 矩阵
- [ ] Lightsail skill 退役另 PR

---

## 12. 审批检查表（人工勾选）

- [x] **D1=A（SSM Hybrid）** — 已确认 2026-10-09
- [x] **D2=Y（全舰队 arm64 / `cax21`）** — 已确认 2026-10-09 晚
- [ ] 接受 **D3=R1（Edge-first）**（方案默认）
- [x] **D4=G1（`fsn1`）** — 已确认 2026-10-09
- [x] **D5：全舰队 `cax21` @ `fsn1`** — 已确认 2026-10-09 晚（取代原 cx33）
- [ ] 接受 **美/英出口改德区** 带来的 RTT / 上游风控变化（过渡期不做多区）
- [ ] 接受「数据不丢 + 可回滚双跑」优先于「全球最低延迟」
- [ ] 确认 §14 凭证就绪（`HCLOUD_TOKEN` 已本机验证；SSH key `tokenkey-hetzner-ops` 已验证；prod/edge 共用同一 token/项目可接受）

---

## 13. 建议的首个实施切片（审批通过后）

1. **PR-1（无云费用）：** Phase-1 骨架 + 单测 + preflight 门禁 + skill 草稿。
2. **PR-2（需费用授权）：** 付费 canary provision + staging DNS。
3. **PR-3：** 单 edge 正式 DNS 切流。
4. **Prod：** 永远单独 PR + 单独维护窗。

### 13.1 Phase-1 落地进度（2026-10-09）

已落仓库（**未切流、矩阵全 `deployable=false`**）：

- `deploy/hetzner/edge-targets-hetzner.json` + `prod-target-hetzner.json`
- `deploy/hetzner/resolve-edge-hetzner-target.py` + 单测（拒非 fsn1 / 非 cax21 / 非 arm）
- `deploy/hetzner/provision-edge.sh`（默认 dry-run；`--confirm-paid` 才建机）
- `.github/workflows/deploy-edge-hetzner-stage0.yml`（`validate` / `provision`）
- **dispatch：** `resolve-edge-deploy-route.py` + `dispatch-edge-deploy.sh` 支持 `--platform hetzner`
- **GHA Secret：** `HCLOUD_TOKEN`（`confirm_paid=true` 时读取；设置见 `deploy/hetzner/README.md`）

**未做：** `rollout-edges` 自动扫 Hetzner、SSM Hybrid Activation、正式 DNS、skill 全文。  
Live edge 仍为 Lightsail（`auto` 仅在 Hetzner `deployable=true` 时改道）。

---

## 14. 凭证与自动化通路（你现在就能准备）

> Console **邮箱/密码 ≠ CLI 凭证**。AWS 类比：账密登录控制台；自动化用 **Access Key /
> OIDC Role**。Hetzner 自动化核心是 **Cloud API Token** +（可选）SSH Key。

### 14.1 必须创建（Hetzner）

| 凭证 | 用途 | 存放 | 谁用 |
|---|---|---|---|
| **Cloud API Token（Read & Write）** × 项目 | `hcloud`、GHA provision/升级、Floating IP、Volume、Firewall | 密码管理器；GitHub Environment：`HCLOUD_TOKEN_EDGE` / `HCLOUD_TOKEN_PROD` | CI + 本机 ops |
| **Cloud API Token（Read Only）**（建议另开） | 日诊、只读巡检、排障 | `HCLOUD_TOKEN_RO` | diagnostics |
| **SSH 公钥**（注册到 Hetzner Project → Security） | rescue / 紧急登录 / D1=B | 私钥仅本机/密管；公钥可进云 | 人 + 受控 runner |
| **项目 ID / 名称** | context 隔离 | 记入 `deploy/hetzner/README.md`（无密钥） | 文档 |

**创建路径（Console）：** Project → Security → API Tokens → Generate  
Token 只显示一次；丢失则作废重建。**不要把 Console 登录密码配给 `hcloud`。**

**本机等价于 `aws configure`：**

```bash
# 安装：https://github.com/hetznercloud/cli
export HCLOUD_TOKEN='……'   # 或
hcloud context create tokenkey-edge
# 粘贴 Read&Write token；之后：
hcloud context use tokenkey-edge
hcloud server list
```

GHA 不写文件 context：workflow 里 `env: HCLOUD_TOKEN: ${{ secrets.HCLOUD_TOKEN_EDGE }}`。

### 14.2 过渡期仍需要（AWS，D1=A 已锁定）

| 凭证 / 资源 | 用途 |
|---|---|
| 既有 GHA OIDC Role（`AWS_OIDC_ROLE_ARN`） | deploy / smoke / diagnostics 假定角色 |
| **SSM Hybrid Activation**（Code + ID，短时） | cloud-init 注册 `mi-*`；激活码用完即废，可轮换新建 |
| SSM Parameter / Secrets 前缀 `/tokenkey/hetzner/…` | edge/prod 运行时密钥（对齐今日 lightsail 前缀隔离） |
| （可选）本机 `aws` 只读 profile | 查 SSM 在线、拉参数；**不是**开 Hetzner 机器所需 |

### 14.3 周边（与今日相同，迁移不新增「Hetzner 账密」）

| 凭证 | 用途 |
|---|---|
| Porkbun（或现行 DNS）API | staging / 正式 A 记录；切流与回滚 |
| GHCR 拉取权限 | 若镜像非 public pull |
| Feishu / 告警 webhook | mem-guard、发布通知（沿用） |
| Admin / 运维 API key | 切 mirror `base_url`、账号探针（沿用现 skill） |

### 14.4 明确不需要交给自动化的

- Hetzner **Console 登录密码** / 邮箱 2FA 备份码（只留在人侧密管）。
- 信用卡全文（账单主体人工确认即可）。
- 旧 Lightsail/EC2 根密钥（回滚窗口内保留 AWS 侧访问即可）。

### 14.5 请你回传给我的最小集合（便于 Phase-1 接线，勿贴明文到聊天）

请确认下列是否已就绪（**是/否** 即可；token 本身放密管或 GitHub Secrets）：

1. Hetzner 项目已建？名称？
2. Read&Write token 已生成并写入密管？
3. Read Only token 是否另开？
4. SSH 公钥是否已上传到 Project？
5. location：**`fsn1` 已确认**。
6. SKU：**`cax21` 已确认**（CLI 建/删验证通过）。
7. 控制面：**D1=A 已确认**。
8. GitHub Environment 是否写入 `HCLOUD_TOKEN`？

**已完成（2026-10-09，未打印密钥）：** `hcloud` 鉴权；SSH `tokenkey-hetzner-ops`；**`cax21@fsn1` create→delete 成功**（新 token）。  
**已拍板：** D1=A、D2=Y（arm）、D4=`fsn1`、D5=`cax21`。

---

## 15. 开放问题（审批时可直接批注）

1. 发票主体 / 2FA 持有人？
2. 账号 Limits：滚动全舰队前 **Primary IPs / S.vCPUs** 是否已申请够（建议 ≥ 现用量 + 8）？
3. （操作）确认 `gh secret list` 可见 `HCLOUD_TOKEN`（设置命令见 `deploy/hetzner/README.md`）。

**已关闭：** Redis 不迁；canary `uk1`→`us5`；冻写 ≤5 min；回滚两档；§17 门禁；  
E3 中国→fsn1 预检；**SKU=`cax21`**；CLI 建机通路（新 token）验证。

---

## 16. 乔布斯复审裁决（数据 × 体验 · 2026-10-09）

| 判决 | 内容 |
|---|---|
| **砍掉的口号** | 「用户体验不受影响」——德区全集群 + 8G 观察期下 **不成立**。只许承诺：不断服、账不错、门禁绿、可回滚。 |
| **数据** | 方向对；危险点仍是 **Prod 写后裸 DNS 回滚**、**冻写不真**、**S3 备份哑火**。用 §17 P 门禁关，不靠自觉。 |
| **体验** | 真杀手是 **上游封 IP（E2）** 与 **切流后错误率（E5/P5）**，不是「慢 50ms」。延迟用 E3 数字说话。 |
| **`cax21` 新坑** | edge 从 Lightsail **x86→arm**：错拱镜像 = 瞬间全挂。用 **E0** 关死。 |
| **运维坑** | Primary IP 限额曾让 API 全线 403；滚动前用 **L0** 检查 Limits 余量。 |
| **可否开工** | Phase-1 骨架：**可以**。正式 DNS：**门禁未绿不行**。 |

---

## 17. 实测关风险（Measure → Gate → Fix）

> **一条命令 · 一个数字 · 一个对策。** 只用现成
> `edge_post_deploy_smoke` / `probe_account_model` / `external_health` /
> `tokenkey-pgdump` / mem-guard / `hcloud`；不新建观测平台。

### 17.1 用法

```text
测 → 红则只做「对策」→ 重测到绿 → 才允许下一动作
```

```text
gate=<ID> before=<n> after=<n> pass=yes|no action=<none|rollback|cap|rotate-ip|...>
```

### 17.2 开跑前（账号 / 镜像 · 一次）

| ID | 风险 | 实测 | 门禁 | 红灯对策 |
|---|---|---|---|---|
| **L0** | 限额再次堵死建机 | Console Limits 或开机前余量 | Primary IPs **空余 ≥ 2**（单台 canary）；全舰队滚动前空余 ≥ 待迁台数 | 删闲置 IP/机，或 Limit increase；**禁止硬扛 403** |
| **E0** | x86→arm 错拱 | staging 拉起后：`docker inspect` 应用镜像 Architecture **或** 部署日志确认 `linux/arm64` | **必须 arm64**；GHCR manifest 含 arm64 | 换 multi-arch tag；禁 `simple_release`；不切流量 |

### 17.3 Edge 门禁（正式 DNS 前 E0+E1–E4 全绿）

| ID | 风险 | 实测 | 门禁 | 红灯对策 |
|---|---|---|---|---|
| **E1** | 通路/TLS/SSM | staging：`edge_post_deploy_smoke.sh` full | infra + oauth + main-via-edge **全绿** | 修 bootstrap/证书/SSM；不切 DNS |
| **E2** | 上游封 IP | `probe_account_model` 真实 OAuth 路径 ×1 | **成功**（非封禁/401 类） | 换 Floating IP 再测；仍红 → **该 edge 留 AWS** |
| **E3** | 德区变慢 | 同探针耗时 vs 旧 edge 基线 | p95 **≤ 基线 × 2** | 暂留 AWS，或签字接受后继续（中国侧预检已过，见下） |
| **E4** | 8G OOM | 探针期 `MemAvailable`（含生图若启用） | 尖峰 **≥ 1.5 GiB** | **降生图并发**；仍红 → **不切**（升配另审批，优先更大 CAX） |
| **E5** | 切后用户挂 | DNS 后 60 min：5xx/上游错/无号 vs 前 60 min | **≤ 基线 + 2pp**；无封禁尖峰 | **立刻** mirror + DNS 回 Lightsail |

**顺序锁死：** L0 → provision → **E0** → E1–E4 → **1 账号 mirror**（≥2h）→ 全量 mirror → 正式 DNS → **E5 值守 60 min**。

### 17.4 Prod 门禁（DNS 前）

| ID | 风险 | 实测 | 门禁 | 红灯对策 |
|---|---|---|---|---|
| **P1** | restore 挂 | 新机 restore 现网 dump | 成功且关键表可查 | 不进维护窗 |
| **P2** | 备份哑火 | 新机 `tokenkey-pgdump`→S3→canary | S3 新鲜 + canary 绿 | 修 Hybrid/IAM；**不切 DNS** |
| **P3** | Volume 误删 | `hcloud` snapshot ≥1 | available | 无快照不开窗 |
| **P4** | 数据不齐 | 冻写后 `max(id)`/`max(updated_at)` + 扣费冒烟 | 游标一致 + 冒烟绿 | 解冻中止；旧库仍为真相 |
| **P5** | 切后故障 | DNS 后 60 min 5xx/计费错 | **≤ 基线 + 2pp** | **写前** DNS 回 EC2；**写后** 禁止裸回，留新库或 `新→旧` 回灌 |
| **P6** | 冻太久 | 计时 | **≤ 5 min**；超时未完成 P4 → 解冻中止 | 另约窗口，不硬扛 |

**Redis：** 不迁。粘性短抖为预期；破 P5 则降并发，不上同步管道。

### 17.5 E3 预检（中国 · 已跑）

出口 `114.254.1.202`。prod=`34.194.234.88`；德区=`fsn1-speed.hetzner.com`（园区测速；其后 CLI 已能建 `cax21`）。

| 目标 | TCP:443 avg | HTTPS total avg |
|---|---:|---:|
| 现 prod | 241 ms | 807 ms |
| fsn1-speed | **185 ms** | **582 ms**（约快 220 ms） |

中国→控制面：**迁德不构成 E3 阻力**。美/英→德区 edge 仍靠 canary E3；风控靠 E2。

### 17.6 关闭口令

| 动作 | 前提 |
|---|---|
| Phase-1 骨架 | 可做 |
| 付费 canary 机 | **L0** 绿 |
| Edge 正式 DNS | **E0–E4** 绿 |
| 下一台 Edge | 上台 **E5** 绿满 60 min + **L0** 仍够 |
| Prod DNS | **P1–P4、P6** 绿；值守 **P5** |
| 对外话术 | 禁止「体验零影响」 |

未绿禁止用「机器起来了 / smoke 过了」代替。
