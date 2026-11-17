# cn-prod deepseek-flash 池压测报告（800 阶梯，2026-11-17）

## 摘要

在已落地的扩容基线上（入口 WRR **1:1**、app-a 4C16G、app-b 4C8G、
`DATABASE_MAX_OPEN_CONNS=80`/应用、PG `max_connections=400`、
`GATEWAY_MAX_IN_FLIGHT=512`），从**专用压测 CVM**对 `deepseek-flash` 做正式阶梯
`400 → 600 → 800`。

| 项 | 值 |
|---|---|
| 日期 | 2026-11-17（BJT 约 13:54–14:17） |
| 站点 | cn-prod |
| 模型 | `deepseek-flash` |
| Shape | Recipe-1 合成 context ≈10k in / `max_tokens=1000`（实测 mean prompt **8861** + completion **1000**） |
| 阶梯 | `PERF_STEPS=400,600,800`，每档 hold **300s**，`success_min=0.98` |
| 入口 | `https://api.galaxywaveai.com` |
| Client | 专用压测机 `ins-7ltl2zv3` / `192.144.136.234`（`tk-cn-loadtest`，SA2.LARGE8 4C8G） |
| 批准令牌 | `APPROVE=yes-formal-tpm-loadtest` |
| Key | `TK_FULLTEST_KEY`（未写入本文；跑后已从压测机删除） |
| 远程产物 | `/tmp/tpm-recipe1/cn-prod-loadtest-800-20261117-135430/` |
| 仓库摘要产物 | `docs/ops/reports/artifacts/cn-prod-deepseek-flash-pool-perf-800-20261117-summary.json` |
| 对照先验 | `docs/ops/reports/cn-prod-deepseek-flash-pool-perf-20261117.md`（app-a 同机 client，200/400） |

**结论一句话：** **400 / 600 / 800 三档全部通过**（成功率 ≥0.9994）；operating point = **800**。
服务未崩、无 5xx 风暴、事后 app/data 健康。软瓶颈仍是 **延迟 / TTFT（及可能的应用 CPU）**；
512 闸门与账号席位未先爆。压测机自身 4C 在高并发下偏热（loadavg1 peak **9.26**），
600 档平均在途偏低，解读吞吐时需打折。

---

## 1. 现网拓扑（本轮）

| 角色 | 配置 | 备注 |
|---|---|---|
| 入口 | WRR **app-a:app-b = 1:1** | 相对先验 3:1 已均衡 |
| app-a | 4C16G，`GATEWAY_MAX_IN_FLIGHT=512` | |
| app-b | 4C8G，同闸门 | |
| 数据机 | PG `max_connections=400`；应用池 80×2 | |
| 供应池 | POOL_SLOTS≈**1045**（channel 45 Volc Agent Plan） | 800/1045≈77% |

本轮**未**改 PG/权重/池席位。

---

## 2. 方法

| 脚本 | 作用 |
|---|---|
| `ops/observability/tpm_burst_recipe1.py` | Recipe-1 `perf` 客户端（压测机本地执行） |
| 远程 runner | `/tmp/run_cn_prod_perf_800.sh`（密钥 stdin 注入，不落仓库） |
| Client host 采样 | 压测机 `/proc/loadavg` + MemAvailable（5s） |
| App/data monitor | `run-probe --node app-a` / `cn-prod-data` + `tpm_burst_monitor_remote.sh`（**压测中长窗 SSM 未收回**；**压测后**短窗快照成功） |

硬门禁：`APPROVE=yes-formal-tpm-loadtest`；禁止 `api-cn.tokenkey.dev`。

---

## 3. 结果

### 3.1 客户端指标

口径：transport 成功（HTTP 2xx、流正常结束、有 usage）。  
TPM 估算：`RPM × (8861 + 1000)`，tok/req ≈ **9861**。  
cache token hit ≈ **99.67%** → TPM 偏乐观。

| 并发 | 成功数 | fail | success_rate | concurrency_mean | RPM mean | RPM peak(60s) | TPM mean 估 | TPM peak 估 | latency p50 | latency p90 | TTFT p50 | content_rate | wall_s |
|---|---|---|---|---|---|---|---|---|---|---|---|---|---|
| **400** | 5790 | 0 | **1.00** | 299.2 | **869.8** | **1407** | **~858 万** | **~1387 万** | 17.7s | 26.0s | 13.1s | 0.77 | 399 |
| **600** | 6280 | 4 | **0.9994** | **252.2** | 598.4 | **1606** | ~590 万 | **~1584 万** | 14.4s | 40.3s | 10.4s | 0.78 | 630 |
| **800** | 5855 | 0 | **1.00** | 676.3 | **1088.3** | **1773** | **~1073 万** | **~1748 万** | **34.2s** | **42.1s** | **24.0s** | 0.77 | 323 |

operating point：**800**（三档均 ≥ `success_min`；runner exit **0**）。

600 档备注：仅 4 次失败（`499` / 连接断开 / `upstream_stream_read_error` / read timeout），
未触发停阶。`concurrency_mean` 明显低于目标，且 `wall_s` 偏长 → **该档平均吞吐不可直接当容量锚点**；
peak RPM 仍可用作上沿参考。

缩放（相对稳健的 400 → 800）：

| 比值 | 400 → 800 |
|---|---|
| 目标并发 | ×2 |
| 平均在途 | ×2.26（299 → 676） |
| RPM mean | **×1.25**（亚线性） |
| RPM peak | ×1.26 |
| latency p50 | ×1.93 |
| TTFT p50 | ×1.83 |

对比先验（app-a 同机 client，400）：当时 RPM mean 837.8 / peak 1162 / lat p50 22.4s / TTFT 17.2s。  
本轮独立 client + 1:1 WRR + app-b 升配后，同 400：**RPM/延迟更好**。

### 3.2 压测机（client）资源

| 指标 | 值 |
|---|---|
| loadavg 1m peak | **9.26**（4C） |
| loadavg 1m p95 | 7.76 |
| MemAvailable min | ≈5.5 GiB（总 7.5 GiB，未 OOM） |

→ 800 并发下 **client 自身已偏热**，后续更高阶梯应先换更大压测机，避免误判网关。

### 3.3 服务侧采样

| 窗口 | 来源 | 观察 |
|---|---|---|
| 压测中长窗 | app-a SSM monitor | **未取回**（本地 poller 在 `command_id` 后提前退出；不把缺失当成健康证据） |
| 压测后 ~1–2 min | app-a 20s snap | loadavg1 **0.57**；app CPU peak **4.7%**；mem **~445MiB/3GiB**；PG total/active **41/1**；Redis connected **162** |
| 压测后 | data 15s snap | loadavg 5m/15m 仍 **~4.8/4.4**（余热）；PG CPU peak **2.7%**；PG conn **41/1**；PG mem ~619MiB/3GiB |

事后服务**健康**，无残留连接打满或进程宕机迹象。

---

## 4. 瓶颈对照（800）

| 层级 | 状态 | 依据 |
|---|---|---|
| 入口 / 传输成功 | **扛住** | 800 成功率 100%；无失败风暴 |
| `GATEWAY_MAX_IN_FLIGHT=512` | **未先爆** | 1:1 下单机份额 ≈400 < 512；无 503 shed 特征 |
| 应用延迟 / 算力 | **软瓶颈** | 800 lat/TTFT 相对 400 近翻倍；RPM 仅 ×1.25 |
| PG 连接池（80×2 / max 400） | **未见打满证据** | 事后 41；压测中峰值**未采到**（缺口） |
| 账号席位 1045 | 未暴露 | 800/1045≈77%；无池耗尽失败模式 |
| 容器内存 3GiB | 未暴露 | 事后 ~445MiB |
| 压测机 client | **已热** | loadavg1 peak 9.26 / 4C |
| 质量旁路 | 仍在 | `content_rate≈0.77`（与先验相近） |

**一句话：** 在当前双应用 + 1:1 + 独立 client 下，**800 正式可过**；继续加并发收益受 **延迟/TTFT（及未采满的应用 CPU）** 制约，而不是 512 闸门或席位。

---

## 5. 与先验 / 扩容清单的对照

先验报告（§6）要求冲 800 前：均衡 WRR、升 app-b、扩 PG 池、独立 client、阶梯 400/600/800。  
本轮验证：**该清单落地后 800 通过**。

| 先验担忧 | 本轮 |
|---|---|
| WRR 3:1 打到单机 512 | 已改 1:1；800 无 shed |
| PG open 40×2 贴顶 | 已改 80×2；事后远低于顶（中峰未知） |
| Client 污染 app CPU | 已迁专用 CVM |
| 应用 CPU 不够 | 延迟上升支持「软瓶颈仍在」；需补**压测中**双机 CPU |

---

## 6. 下一步建议

1. **容量锚点**：把 **800 / RPM peak≈1773 / TPM peak≈1748 万（高 cache）** 记为当前正式上沿；对外引用注明 cache hit 乐观偏差。  
2. **冲 1000+ 前**：  
   - 压测中挂稳 **app-a + app-b + data** monitor（`run-probe --node`，勿依赖会早退的长 poll）；  
   - 压测机升到 ≥8C，或确认 client load 不再先饱和；  
   - 若单机 in-flight 逼近 512：加第 3 应用机，或审慎上调闸门并配内存。  
3. **不必先做**：仅为过 800 再扩席位；本轮席位与内存仍有余量。  
4. **跟进**：`content_rate≈0.77` 与 600 档少量 upstream/超时噪声的根因（非本轮阻断）。

---

## 7. 服务健康（跑后）

- runner exit **0**；无中止剩余阶梯。  
- app-a / data 短窗 snap：**正常**。  
- 压测机保留（`expire_after=2026-11-19`）；**未销毁**。  
- 远程 `/tmp/tk-fulltest.key` **已删除**。

---

## 8. 相关路径

- 先验 200/400：`docs/ops/reports/cn-prod-deepseek-flash-pool-perf-20261117.md`
- 摘要 JSON：`docs/ops/reports/artifacts/cn-prod-deepseek-flash-pool-perf-800-20261117-summary.json`
- Client 峰值：`docs/ops/reports/artifacts/cn-prod-deepseek-flash-pool-perf-800-20261117-client-host.json`
- 配方：`ops/observability/tpm_burst_recipe1.py`
