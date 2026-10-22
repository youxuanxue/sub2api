# cn-prod → 腾讯云北京迁机 plan（瘦路径）

状态：`phase=plan`（2026-10-22；对齐 `tokenkey-host-replacement` 单窗纪律）  
已同意：北京轻量 **4C8G**、不够只 **垂直升配**、**终态**新主 **pgBackRest**、不做水平多副本。  
**入口：`api-cn.tokenkey.dev` 不动**——继续与海外 `api` 同入口；本次 **无公共 DNS 变更**。  
**交付范围：** 北京机接写 + 控制面切到新静态 IP；打 `api-cn` 的流量仍在海外，**不在本次验收范围**。验收入口 = 新 `public_origin`（IP）及已知直连该 IP 的客户/运维。  
**未授权：** 建付费实例、切流、`--apply` 物理备份。

意图：**机与库搬到腾讯北京 4C8G；迁机关门时新主已具备已验收物理备份；不为工具叙事在 UCloud 上预装 pgBackRest。**

对照技能：[tokenkey-host-replacement](../../.cursor/skills/tokenkey-host-replacement/SKILL.md)。cn-prod **例外**：切流用逻辑 dump（非 standby/promote）；**不例外**：布局/控制面/告警同战役收口，禁止「先切流、备份下周再说」。

## 1. 规格与成本

| 项 | 值 |
|---|---|
| 源 | UCloud `uhost-1wgyn7gu2f6a`，O **8C/8G**，~¥694/月 + 流量 |
| 目标 | 腾讯北京轻量 **4C8G**（锐驰或通用），~¥230/月 |
| 形态 | 单节点 Stage0；无 SLB / 托管库 / 多副本 |
| 架构 | **amd64** |

## 2. 顺序（硬）

```
rehearse / prepare（北京机布局=终态；至少一次 dump→恢复+账务演练）
    → 同一战役日（窗 A+B 连续 epoch）：
         停写 → 最终 dump → 新主接写 → 改 IP / SSOT / 告警
         → 新主装 pgBackRest → 最新+历史恢复验收通过
    → 宣布迁机 verify 关门
    → 另批：停 cn-prod pgdump（保留期）
    → 旧 UCloud ≥7 天后退役
```

| 做 | 不做 |
|---|---|
| 布局 prepare 即终态；当日可 `deploy_via_ssm.sh` + `bluegreen.py` | UCloud 上装/验 pgBackRest 当前置 |
| 迁机关门 = **物理备份验收通过** | 窗 A 接写后把装仓拖成下个迭代 |
| 只改静态 IP / `prod-target` / 监控 | 改 `api-cn` DNS；拿海外 `api-cn` 绿冒充北京机绿 |
| amd64 独立验收 | 用美西 ARM 回执充数 |

默认切流：**最终 pgdump → 新主恢复**。体积/耗时若不可接受，另批物理辅助——不默认 UCloud Phase 0。

**窗 A→B 保护缺口：** 接写后至首份物理全量入仓前，RPO 依赖「尚未退役的源侧 pgdump / 未完成的新链」。战役日内必须收口 B；不得以「另批」跳过装仓与验收。

## 3. 终态块（plan 一次写清）

### 3.1 DB

- 源写入隔离（应用、后台、直连、相关 timer）；默认只读 ≠ 完整隔离；严禁双写。  
- 最终命名 dump → 新主恢复 + 账务抽检 → 再启目标写者。  
- 接写后禁止解冻旧库回滚。  
- 物理终态见 §3.4；逻辑切流是 cn-prod 路径例外，不是备份终态例外。

### 3.2 布局 / 发版（切流当日必须满足）

| 项 | 要求 |
|---|---|
| `.env` | `/var/lib/tokenkey/.env` |
| 网关 | `tokenkey` / `tokenkey-caddy`（与国内 bluegreen 一致） |
| PG / Redis | Stage0 名或别名 `postgres` / `redis` |
| Docker Root | **默认不搬**；若例外保留，写入 domestic SSOT |
| 发版 | verify 后即可国内 SSM 蓝绿（**不是**海外 `deploy-stage0`） |

无 post-cutover 布局停机。

### 3.3 控制面（失败关闭）

| 项 | 终态 |
|---|---|
| 身份 | `deploy/domestic/prod-target.json`：新实例 ID、新 `public_origin`、新 `mi-*` |
| 发版 / probe | `deploy_via_ssm.sh`、`run-probe.sh --target cn-prod` **只**解析新清单 |
| 镜像分发 | CFN / lifecycle 中 `ManagedInstanceId`（及等价绑定）切到新 mi |
| 旧身份 | 旧 `mi-0efdf781205cff512` / 旧 UHost：**默认发版与日常 probe 失败关闭**（只读保留窗另批） |
| 海外 | **不**改 `PROD_SSM_TARGET` / 美西矩阵 |
| 契约 | cutover **当日**改 domestic README、本 plan 状态、必要 handoff；不过夜假「未切流」 |

### 3.4 备份 / 告警（同战役，迁机关门条件）

- 独立桶/前缀（禁止 cn **pgdump** 生命周期桶、禁止 prod 活前缀）。  
- amd64 备份镜像；无 IMDS → 短期身份 / Hybrid；`RuntimeDirectoryPreserve=yes`。  
- 全量 + WAL + 增量；隔离 **最新点 + 历史同点** 恢复 + 账务回执。  
- 飞书宿主告警与 `host_https_watch`（`WATCH_ROLE=cn-prod`）接到新机；观察点里 **cn-prod IP** 换新（`api-cn` 域名探针可仍指海外）。  
- 仓区（AWS 异地 vs 北京 COS）**prepare 前选定**；未选型不建仓。COS 工具改造单列，可与 rehearse 并行，但 **挡迁机关门** 若选了 COS 却未就绪。  
- **停 `ops-cn-pgdump`**：物理验收通过后 **另批**（对象保留期对齐 prod 精神）。

### 3.5 DNS / 入口

- **无公共 DNS 变更**；无 TTL 操作。  
- Verify：对新 IP 做 TLS + `/health`；需要时 `curl --resolve` 指向新 IP。  
- **禁止**用 `api-cn.tokenkey.dev`（海外）的健康结果关闭北京机验收。

### 3.6 出口

- 默认不同窗改 `proxy_id`；主机切流 ≠ 出口切流。

## 4. 战役步骤

### rehearse / prepare（授权建机后）

1. 建北京 4C8G；布局按 §3.2 起栈（禁止 `formal/` + 临时容器名当终态）。  
2. 不出站业务写者、不改 DNS、不接生产流量。  
3. **至少一次**完整：源侧（或演练用）dump → 本机恢复 → 账务抽检回执。  
4. SSM Hybrid 注册；发版/probe 路径对 **演练 mi** 打通（切流再切清单）。  
5. 窗 B 仓区与桶/前缀设计定稿（可先建空仓与身份，不启生产 archive）。

### 窗 A — 切流（维护窗）

1. 锁定发版；盘点并冻结写者。  
2. 最终 dump → 新主恢复 + 账务抽检。  
3. 启目标写者；更新 `prod-target.json` / 镜像分发 mi / 告警与 watch 的 **IP 目标**。  
4. §3.3 失败关闭旧身份。  
5. **不**改 `api-cn`。  
6. **不**在此步宣布迁机完成——进入窗 B。

### 窗 B — 物理备份（同一战役日 / 连续小时）

1. 新主 `--apply` 物理备份；首全量 + WAL + 增量。  
2. 隔离最新 + 历史同点恢复验收。  
3. `archive_check` / 探针认识新链。  
4. **此时**才可宣布迁机 verify 关门。

### retire（另授权）

- 停 cn-prod pgdump 调度；保留对象约定期限。  
- ≥7 天后 UCloud 退役。

## 5. verify

**迁机关门（A+B）：**

1. 新 IP：可信 TLS、`/health` `status=ok`（非 `api-cn` 域名冒充）。  
2. 经新 IP 的真实 UI 与至少一笔境内补全 + usage 归属。  
3. fence/usage 抽检；错误区分上游 vs 平台（对照 `ops_error`）。  
4. SSM 发版/probe 打到 **新 mi**；旧 mi 默认路径失败。  
5. 布局 §3.2；国内蓝绿可 prepare（可烟测不切流）。  
6. 物理：最新+历史恢复回执 + archive 健康。

**不在范围：** `api-cn` 海外入口行为（除非另开 DNS 窗）。

## 6. 阶段门禁

| 阶段 | 门禁 |
|---|---|
| plan | 本页 |
| rehearse / prepare | 建机授权；布局=终态；**恢复演练回执**；仓区已定 |
| cutover 战役（A+B） | 维护窗授权；**物理验收通过** = 迁机关门 |
| retire | 另批停 pgdump；≥7 天退 UCloud |

## 7. 下一步（待授权）

1. 选锐驰 vs 通用 4C8G + 可用区；授权 rehearse/prepare 建机。  
2. 选定窗 B 物理仓区（AWS vs COS）。  
3. `api-cn` 回国内：将来单独窗，不绑本闭环。
