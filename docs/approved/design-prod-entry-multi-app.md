---
title: 美西 prod 入口机 + 双应用机，旧应用机脱离 LUKS
status: approved
approved_by: "feng（2026-11-15 会话：批准设计）"
approved_at: 2026-11-15
risk: high
authors: [agent]
created_at: 2026-11-15
revised: 2026-11-20（L1–L8 / X1–X4 / DNS 切流已完成；删除执行表，仅保留现网架构与滚动契约）
---

# 美西 prod 入口机 + 双应用机

本文是**已批准**现网基线。阶段 A–D、DNS 切流、L1–L8 脱离 LUKS、X1–X4 销毁载体均已完成
（2026-11-18/19，见 `docs/decisions/decided.md`）。`volume_platform=tencent-root`；
**禁止恢复 LUKS 解锁路径**。舰队身份见 [`deploy/hetzner/README.md`](../../deploy/hetzner/README.md)。

相对 [prod 应用机并行替换](design-prod-app-parallel-replacement.md)：

- **接管**：入口、切流、滚动发版、脱离 LUKS（本文）。
- **仍有效**：双应用机保留、不中断请求、QA 集中管理与 24h 丢失。

滚动实现与 cn-prod 共用：[cn-prod 滚动发版](design-cn-prod-rolling-deploy.md)。

## 现网决定（摘要）

| 项 | 现网 |
|---|---|
| 布局 | 单入口机 + 双应用机 + 数据机；应用机无公网 80/443 |
| 入口 | 单入口、无热备；故障靠重建（离机证书备份） |
| 发版 | 多应用机滚动，不留节点内蓝绿；TAT 为主 |
| 卷 | 明文根盘 `tencent-root`；LUKS 已销毁 |
| QA >24h | 强制清理可接受丢失（见并行替换文档 / `ops/qa`） |

## 2. 目标架构

```
客户端 ──DNS(5 域名, TTL 60)──▶ 入口机 2C4G Caddy :443
                                    ├──▶ 旧应用机 4C16G 10.4.0.16:8080（明文根盘，权重 1）
                                    └──▶ 候选应用机 4C8G 10.4.0.9:8080（明文根盘，权重 1）
两台应用机 ──▶ 数据机 10.4.0.3 PG/Redis
两台应用机 ──公网 IP──▶ cn-ip5..12 / Oregon SOCKS

应用机公网 80/443：切流稳定后关闭（同 cn-prod）
入口故障：重建入口机；不做旧机热备入口 / 证书回推
（历史切流期曾 cand=2C8G、WRR 2:1；以矩阵为准）
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

- 复用 `deploy/stage0/Caddyfile` 与渲染器；增加「后端列表 + 权重」：上游改为两台应用机私网 8080，权重来自舰队矩阵 `apps[].weight`（渲染为 `lb_policy weighted_round_robin …`），`health_interval 3s`，`health_fails 2`；保留 `lb_try_duration 30s` 等。
- **现网（2026-11-18）**：cand 已升配 4C8G 后入口 WRR **1:1**（`10.4.0.16`×1 + `10.4.0.9`×1；见 `candidate-usw-20261022.json` 与 decided.md）。历史切流窗口曾为 `2 1`（旧机偏重），不是今日现状。
- 只用主动健康检查。`PROD_EDGE_REQUIRED_ENV` 与 `import tokenkey_api_machine` 门禁对入口机生效。
- 客户端 IP：入口机写 `X-Forwarded-For`/`X-Real-IP`，删 `CF-Connecting-IP`。应用机 8080 只放行入口机私网 /32。经 socat 后网关记录的 IP **未核实**，切流前用测试请求实测。

### 2.3 应用机私网入口

每台应用机：单容器 `tokenkey` 监听 `<private_ip>:8080`（与 cn-prod 终态一致）。
**2026-11-17 M6**：滚动已就绪，节点内 socat/蓝绿发版路径退役（与 cn-prod M6 对齐）；
`deploy_via_ssm_bluegreen.sh` / `legacy-deploy` 默认失败关闭，仅
`STAGE0_ALLOW_LEGACY_BLUEGREEN=1` 可应急。
不往应用机 Caddyfile 加私网站点。

### 2.4 应用机规格与连接预算

**现网（2026-11-18）**以舰队矩阵与 [hetzner README](../../deploy/hetzner/README.md) 为准：

| 节点 | 规格 | mem / GOMEMLIMIT | 权重 |
|---|---|---|---|
| 旧 `app-old` 4C16G | 明文根盘 `tencent-root` | 矩阵 `memory_limit`（现 8g） | 1 |
| 候选 `app-cand` 4C8G | 明文根盘（2026-11-18 由 2C8G 升配） | 矩阵显式给出 | 1 |

PG：稳态约 75；滚动时一台摘流，无双色重叠。LUKS 已销毁；现网两台明文应用机按入口 WRR 承载。

### 2.5 多节点共享状态门禁

接流前实测：OAuth/登录 session、凭据刷新锁、认证缓存失效、并发/RPM、调度租约、后台单例。
`model_pricing.json` / `plugins/` 是否仅本机缓存**未核实**。媒体已排除。


## 3. 脱离 LUKS（已完成）

L1–L8 与 X1–X4 已于 2026-11-18/19 完成；执行表与 LUKS 回退路径已删除。
现网仅明文根盘；不得重建 `tokenkey-luks-unlock`、载体文件或操作员 vault。

## 4. 切流（已完成）

五域名已切到入口机；旧应用机公网 80/443 已关。入口故障不靠 DNS 回旧机，靠重建入口机。

## 5. 发版：多应用机滚动（与 cn-prod 共用，prod 先对齐布局）

1. **节点矩阵**：每节点 `lhins-*`、`mi-*`、TAT/SSM region、私网、hostname、卷契约、内存档、权重、QA source id。执行前双通道身份核对。
2. **布局**：prod 与 cn-prod 均为单容器滚动布局（路径/compose 由矩阵与 `rolling` 参数化）；禁止节点内蓝绿。
3. **内存档**：矩阵显式给出；2C8G 缺省或 8g → 拒绝。
4. **逐节点**：确认它机健康 → drain → 换镜像 → loopback 烟测 → 私网 Host 头检查 → 入口回流 → 下一台。失败停，不自动回滚已完成节点。公网 canary 不能代替逐节点检查。
5. **不留节点内蓝绿**。双节点时一台摘流、另一台承载；2C8G 单台门禁未过前，不把旧机从入口摘掉做破坏性维护。
6. **跨节点锁**：入口机租约或等价物；CI 与手工共用。
7. **TAT 主路径**：日常扇出默认 TAT；SSM 仅 break-glass。
8. **边缘**：发版只改应用机；`sync_caddyfile` 目标改为入口机。
9. **镜像**：滚动前全节点预拉并校验 digest。
10. **版本门禁**：全节点同一 tag 才开始；候选机升版前不得接流。
11. **cn-prod 教训写入实现**：Docker 网络在 compose 外创建时声明 `external_network`；Caddyfile 目录挂载。


## 6. 风险（仍有效）

- 入口单点；无热备入口；恢复靠重建。对外口径见 `docs/decisions/decided.md` 与舰队 README「高可用与等效容灾」。
- `.env`/QA 落明文根盘（与现网一致）。
