# cn-prod deepseek-flash 池压测报告（2026-11-17）

## 摘要

在 cn-prod「入口机 + 双应用机 + 数据机」布局下，对 `deepseek-flash`（实际走
VolcEngine Agent Plan，`channel_type=45`）做正式池压测。客户端放在 **app-a**，经公网入口
`https://api.galaxywaveai.com` 回打，避免本机上行瓶颈。

| 项 | 值 |
|---|---|
| 日期 | 2026-11-17 |
| 站点 | cn-prod |
| 模型 | `deepseek-flash` |
| 供应池 | `china_deployed` + newapi channel 45（Volc Agent Plan） |
| Shape | 合成 context ≈10k prompt 目标（实测 mean prompt **8861**）+ `max_tokens=1000` |
| 阶梯 | `PERF_STEPS=200,400`，每档 hold **300s**，`success_min=0.98` |
| 入口 | `https://api.galaxywaveai.com` |
| Client / monitor 宿主 | app-a（`lhins-6qmb3fd6` / SSM `mi-06568963b4b3c3239`） |
| 批准令牌 | `APPROVE=yes-formal-tpm-loadtest` |
| Key | `TK_FULLTEST_KEY` → 须绑定 `china_deployed`（未写入本文） |
| 产物 | `/tmp/tpm-recipe1/cn-prod-remote-20261117-102420/` |

**结论一句话：** 400 并发下服务未崩（成功率 100%），但应用机 CPU/TTFT 已成软瓶颈；账号席位与容器内存远未触顶。冲 800 前须先扩应用面与数据连接配额（见文末清单）。

---

## 1. 现网拓扑与池席位（压测前只读）

> 本节为 **2026-11-17 正式压测当时**拓扑。压测后已落地：app-b→4C8G、WRR **1:1**、应用池 80；见 §6.4 执行状态。

| 角色 | 身份 | 备注 |
|---|---|---|
| 入口 | `10.2.4.7` / `154.8.185.224` | Caddy TLS；WRR **app-a:app-b = 3:1**（压测时） |
| app-a | 4C16G，`memory_limit=3g`，权重 3 | 单容器；`GATEWAY_MAX_IN_FLIGHT_REQUESTS=512` |
| app-b | 2C4G，`memory_limit=3g`，权重 1 | 金丝雀位（压测后已升 4C8G） |
| 数据机 | `10.2.4.5` | PG / Redis / 备份 |

`china_deployed` 上 deepseek-flash 表面供应：

| 项 | 值 |
|---|---|
| 原生 DeepSeek `channel_type=43` | **0** 账号 |
| 实际供应 | channel **45**（Volc Agent Plan），映射 deepseek-flash |
| 可调度账号 | 209 |
| **POOL_SLOTS（席位）** | **1045**（≈209×5） |
| 原生 43 池 | 不适用本轮 |

探针：`ops/observability/probe-cn-prod-deepseek-pool-slots.sh`（经 `run-probe --target cn-prod-data`）。

---

## 2. 方法与脚本

| 脚本 | 作用 |
|---|---|
| `ops/observability/tpm_burst_recipe1.py` | Recipe-1 `perf` 客户端 |
| `ops/observability/tpm_burst_perf_remote.sh` | 应用机上执行 perf + 本机 monitor |
| `ops/observability/tpm_burst_monitor_remote.sh` | 主机/连接采样 |
| `ops/observability/run_cn_prod_perf_local_driver.sh` | 本机注入 key 并 SSM 投递（不打印密钥） |

硬门禁：

- 禁止 `api-cn.tokenkey.dev`（美西 DNS）
- 真流量需要 `APPROVE=yes-formal-tpm-loadtest`
- 客户端使用空 `ProxyHandler`，绕过本机系统代理

本机上行曾测：`networkQuality` 上行约 42 Mbps，但对 cn-prod 直连突发有效吞吐明显更低，且存在 VPN/`Connections Proxied: 100%`；故正式跑改为 **应用机 client**。

---

## 3. 结果

### 3.1 客户端指标

口径：transport 成功（HTTP 2xx、流正常结束、有 usage）。  
TPM 估算：`RPM × (prompt_tokens_mean + completion_tokens_mean)`，tok/req ≈ **9861**。  
合成 filler **cache hit ≈ 99.7%**，TPM 偏乐观；真实多样 prompt 会更低。

| 并发 | 成功数 | success_rate | RPM mean | RPM peak(60s) | TPM mean 估 | TPM peak 估 | latency p50 | latency p90 | TTFT p50 | content_rate |
|---|---|---|---|---|---|---|---|---|---|---|
| 200 | 3797 | **1.00** | 708.6 | 880 | ~699 万 | ~868 万 | 15.5s | 19.8s | 10.8s | 0.77 |
| **400** | 4861 | **1.00** | **837.8** | **1162** | **~826 万** | **~1146 万** | **22.4s** | **28.4s** | **17.2s** | 0.78 |

operating point：**400**（两档均 ≥ `success_min`）。

缩放：

| 比值 | 200 → 400 |
|---|---|
| 目标并发 | ×2 |
| 平均在途 | ×1.83 |
| RPM mean | **×1.18**（亚线性） |
| latency p50 | ×1.45 |
| TTFT p50 | ×1.59 |

### 3.2 主机 monitor（app-a，900s 窗）

| 指标 | Peak / 末值 |
|---|---|
| loadavg 1m | **24.7**（含本机 client，偏高估） |
| app CPU | **342%**（4C） |
| 容器内存 | **420.5MiB / 3GiB** |
| TCP established | 405 |
| PG total / active conn | 81 / 67 |
| Redis clients | 77 |

对照配置（`deploy/domestic/compose.override.yml`）：

- `DATABASE_MAX_OPEN_CONNS=40` × 2 应用 ≈ **80** → 实测 81 **已贴应用侧连接池顶**
- `REDIS_POOL_SIZE=96` → 尚未先爆
- `GATEWAY_MAX_IN_FLIGHT_REQUESTS=512` → WRR 下 400 时 app-a 份额 ≈300，**未触发 503 shed**

---

## 4. 400 并发已暴露的瓶颈

| 层级 | 状态 | 依据 |
|---|---|---|
| 应用机 CPU / 调度延迟 | **已暴露（软瓶颈）** | TTFT/latency 明显上升；RPM 仅 ×1.18 |
| 数据机 PG 连接池 | **临界** | total conn 81 ≈ 40×2 |
| 账号席位 | 未暴露 | 400/1045 ≈ 38% |
| 进程 in-flight 闸门 | 未暴露 | 热节点未到 512 |
| 容器内存 | 未暴露 | 420MiB / 3GiB |
| 入口机 | 本轮无失败证据 | 未单独采入口 CPU/连接 |
| 质量 | 旁路信号 | `content_rate≈0.78`（transport 成功但可见正文偏少） |

**一句话：** 400 时系统抗住；吞吐已由 **应用机算力 + 延迟** 决定，而不是席位或内存。

---

## 5. 分层容量预估（本 shape）

公式：\(\mathrm{RPM} \approx C \times 60 / L\)，\(\mathrm{TPM} \approx \mathrm{RPM} \times 9861\)。  
下列「实用」锚定本次实测；「硬顶」为配置/席位公式。

| 层 | 实用并发（估） | 实用 RPM（估） | 实用 TPM（估） | 硬顶 / 备注 |
|---|---|---|---|---|
| 入口 | ≥400（未测满） | ≥1100 | ≥1100 万 | 通常高于单应用 CPU；需单独压 |
| 应用机（双机现状） | **~400–600** | **~800–1500** | **~800 万–1500 万** | 闸门：热节点先满时全集群 ≈ **683**（512×4/3）；两机都满 = 1024 |
| 数据机 | 随应用 | 撑住本次 RPM | — | 先扩 `DATABASE_MAX_OPEN_CONNS` / PG `max_connections` |
| 账号池 | **1045** | 公式随 L：~2200–4200 | 公式 ~0.2–0.4 亿 | 席位上界；上游限流可能更早 |

**已验证锚点：** C=400 → RPM peak **1162**，TPM peak **~1146 万**，成功率 100%。

---

## 6. 冲 800 并发前：保守调整清单

目标：800 在途经入口进入双应用机时，**优先避免** 热节点 503、连接池打满、单机 CPU 塌缩；数字取保守侧。

WRR 3:1 下，全集群 800 ≈ **app-a 600 + app-b 200**。

### 6.1 必须先做（否则 800 会打到已知硬边）

| # | 调整 | 为什么（保守） | 建议值 / 动作 |
|---|---|---|---|
| 1 | **打破 WRR 热节点 512 闸门** | 800 时 app-a ≈600 **> 512** → 大量 **503 + Retry-After**，测的是 shed 而非池容量 | **任选其一（保守推荐 A）**：**(A)** 加第 3 台应用机并降单机权重，使任一节点 in-flight ≲ 400；**(B)** 临时把两台权重改为更均衡且两机规格对齐后再打；**(C)** 仅升 `GATEWAY_MAX_IN_FLIGHT_REQUESTS`（如 768–1024）**且**确认 3GiB 内存与 CPU 够 —— 单升闸门最激进，保守路径不首选 |
| 2 | **提高应用侧 PG 连接池** | 400 时已 81 ≈ 40×2 | 每应用 `DATABASE_MAX_OPEN_CONNS`：**40 → 80**（两应用合计 160）；同步确认数据机 `max_connections` 与头寸（保守留 ≥2× 应用合计 + 运维余量） |
| 3 | **应用算力** | 400 时 app-a CPU 已高；800 份额更大 | 保守：**新增至少 1 台与 app-a 同档（≥4C）应用机**，或把 app-b 升到与 app-a 同档并改权重；不要只靠调大 in-flight |
| 4 | **Client 迁出应用机** | 本轮 loadavg/CPU 含 400 线程 client | Client 放到 **独立压测机**（同 VPC 或同城），monitor 分挂 entry / 各 app / data |

### 6.2 强烈建议（降低误判与次生故障）

| # | 调整 | 说明 |
|---|---|---|
| 5 | Redis | `REDIS_POOL_SIZE` 按应用数预留（例如每应用 96→128），并看 data 机 `maxclients` / 内存 |
| 6 | 入口观察 | 800 前装好/确认 `cn-prod-entry` watch；压测中采入口 CPU、活跃连接、5xx |
| 7 | 数据机观察 | 压测中采 PG CPU、连接、锁等待、Redis CPU/evicted；不要只看应用侧 |
| 8 | 阶梯 | 不要直接 800；保守阶梯 **`400,600,800`**，每档 hold≥300s，`success_min` 仍 0.98；任一档成功率跌破即停 |
| 9 | 账号池 | 800/1045≈77%，一般够；若 `pool_load_rate` 逼近 90% 再补号。本轮不是第一瓶颈 |
| 10 | 内存 | 3GiB 在 400 时很空；加应用机后仍观察。仅当提高 in-flight 且单机堆高并发时，再评估 `TOKENKEY_APP_MEMORY_LIMIT` / `GOMEMLIMIT` 成对上调 |

### 6.3 明确可以暂缓

- 仅为 800 去扩账号池到远超 1045（除非补号成本极低且 `pool_load_rate` 已高）
- 入口机先扩容（无本轮证据）；有入口 5xx/连接耗尽证据再动
- 把 `max_in_flight` 加到很大却不增加应用机（会把 OOM/CPU 塌缩风险从「503」变成「整机坏」）

### 6.4 已拍板的变更集（2026-11-17）

用户决定（见 `docs/decisions/decided.md`）：

1. **维持 2 台应用机**；app-b 升到 **`bundle_rs_mc_lg8_01` 4C8G**（非 4C16G）；入口 WRR **1:1**  
2. **专用压测机**临时约 **2 天**（北京按量 4C8G CVM；不放代理机/应用机）  
3. **`DATABASE_MAX_OPEN_CONNS` 40→80**/应用（idle 10→20）；数据机 **`max_connections` 200→400** 须**低峰审批后**才重启 PG

仓库已改：`deploy/domestic/compose.override.yml`、`prod-target.json` 权重、入口 Caddy 模板/testdata。  

#### 执行状态（2026-11-17）

| 项 | 状态 |
|---|---|
| app-b 升配 4C8G + 容器回流 `/health=200` | **已完成**（`nproc=4`，`Mem≈7.6G`） |
| 入口权重 3:1 → **1:1** + Caddy reload | **已完成**（`a=200 b=200`） |
| 现网 `DATABASE_MAX_OPEN_CONNS=80` / idle=20（两应用 recreate） | **已完成** |
| 北京按量压测机 `SA2.LARGE8`（约 2 天，tag `expire_after=2026-11-19`） | **已创建**：`ins-7ltl2zv3` / 公网 `192.144.136.234` / 私网 `172.21.0.17`（`ap-beijing-7`，约 ¥0.5/h；密码在操作机 `/tmp/cn-loadtest-password.txt`，**不入库**） |
| 数据机 PG `max_connections` 200→400 | **已完成**（2026-11-17 13:46 BJT，`SHOW max_connections=400`；见 `docs/ops/approvals/cn-prod-pg-max-connections-200-to-400.md`） |

800 时单机约 400 路，仍低于 512 闸门，但 CPU 是否够要用独立 client 再压证明。
---

## 7. 飞书扩容信号（与本次相关的已有/缺口）

| 信号 | 状态 | 扩容含义 |
|---|---|---|
| `gateway_inflight_usage_percent` ≥95% 持续 2m | 已 seed（`tk_113`） | 扩应用机 / 降入站 |
| `pool_load_rate` ≥90% 持续 5m | 已 seed（`tk_031`） | 补号 |
| DB/Redis 不可用 | 已 seed（`tk_112`） | 数据依赖事故 |
| 应用 CPU / 入口 5xx / PG 连接池 | **连续规则仍弱** | 本轮证明 CPU 与 PG open 值得做成前瞻告警 |

---

## 8. 复现命令（要点）

```bash
# 席位只读
bash ops/observability/run-probe.sh \
  --target cn-prod-data \
  --script ops/observability/probe-cn-prod-deepseek-pool-slots.sh \
  --expected-instance-id mi-084a456b3dee71bb4

# 应用机正式压测（本机 driver；密钥来自 TK_FULLTEST_KEY，不打印）
OUT_DIR=/tmp/tpm-recipe1/cn-prod-remote-$(date +%Y%m%d-%H%M%S) \
  bash ops/observability/run_cn_prod_perf_local_driver.sh
```

driver 内默认：`MODEL=deepseek-flash`，`PERF_STEPS=200,400`，`POOL_SLOTS=1045`，`APPROVE=yes-formal-tpm-loadtest`。

---

## 9. 残留风险与未验证项

- Client 与 app 同机 → 主机 CPU/loadavg **不能**当作纯网关容量 SSOT  
- 高 cache hit → TPM 偏乐观  
- app-b、入口、数据机 CPU 本轮采样不足  
- `content_rate≈0.78` 未根因  
- 800 并发 **未跑**；文末清单落地前不建议打

---

## 10. 相关路径

- 舰队 SSOT：`deploy/domestic/README.md`、`deploy/domestic/prod-target.json`
- 应用池 env：`deploy/domestic/compose.override.yml`
- 容量飞书 seed：`backend/migrations/tk_113_seed_capacity_scaleout_alerts.sql`
- 账号池告警 seed：`backend/migrations/tk_031_seed_pool_load_rate_alert.sql`
