# cn-prod deepseek-flash 池压测报告（1200 阶梯 / 亚线性根因，2026-11-17）

## 摘要

在 800 正式通过后，按决策将压测机升到 **8C16G**，再跑阶梯 `400 → 800 → 1200`，
专门验证「并发翻倍、RPM 明显亚线性」的根因。

| 项 | 值 |
|---|---|
| 日期 | 2026-11-17（BJT 约 14:57–15:13） |
| 站点 | cn-prod |
| 模型 | `deepseek-flash` |
| Shape | Recipe-1 ≈10k in / `max_tokens=1000`（实测 prompt **8861** + completion **1000**） |
| 阶梯 | `PERF_STEPS=400,800,1200`，每档 hold **300s**，`success_min=0.98`，`--pool-slots 2000` |
| 入口 | `https://api.galaxywaveai.com` |
| Client | `ins-7ltl2zv3` / `192.144.136.234`（`SA2.2XLARGE16` **8C16G**） |
| 批准令牌 | `APPROVE=yes-formal-tpm-loadtest` |
| 远程产物 | `/tmp/tpm-recipe1/cn-prod-loadtest-1200-20261117-145713/` |
| 仓库产物 | `docs/ops/reports/artifacts/cn-prod-deepseek-flash-pool-perf-1200-20261117-*.json` |
| 对照 | `…perf-800-20261117.md`（同日 4C client，400/600/800） |

**结论一句话：**

1. **正式可过上沿仍是 800**（本轮 800 成功率 100%；operating point = **800**）。  
2. **1200 成功率 20.2% 主要是压测机 soft `ulimit -n=1024`（Errno 24 Too many open files）**，不是网关先崩；网关侧仅 **28** 次 `503 overloaded_error`。  
3. **400→800 的亚线性吞吐，根因是延迟/TTFT 随并发抬升（Little’s Law）**，不是「4C client 不够」——升到 8C 后同档 RPM 仍近似平台。  
4. 服务侧在高压窗**偏热但未挂**：双应用 CPU 打满量级、数据机 loadavg 一度 **22**、PG 瞬时 **172/162**（相对 `max_connections=400` 仍有余量）。公网全程 `/health=200`。

---

## 1. 现网拓扑（本轮未改）

| 角色 | 配置 |
|---|---|
| 入口 | WRR **app-a:app-b = 1:1** |
| app-a | 4C16G，`GATEWAY_MAX_IN_FLIGHT=512`，容器 mem 3GiB |
| app-b | 4C8G，同闸门 |
| 数据机 | PG `max_connections=400`；应用池 80×2 |
| 供应池 | deepseek-flash Volc channel；本轮 runner `--pool-slots 2000`（避免 1200 被席位过滤） |

---

## 2. 方法

| 脚本 / 通道 | 作用 |
|---|---|
| `ops/observability/tpm_burst_recipe1.py` | Recipe-1 `perf`（压测机本地） |
| 远程 runner | `/tmp/run_cn_prod_perf_1200.sh` |
| Client host 采样 | `client-host.jsonl`（5s） |
| App lite | app-a SSM `/tmp/tpm-mon/lite.jsonl`；app-b TAT 同路径 |
| Data lite | cn-prod-data SSM `/tmp/tpm-mon/lite.jsonl`（`pg_total_active`） |

硬门禁：`APPROVE=yes-formal-tpm-loadtest`；禁止 `api-cn.tokenkey.dev`。Key 未写入本文。

---

## 3. 客户端结果

口径：transport 成功（HTTP 2xx、流正常结束、有 usage）。  
TPM 估：`RPM × 9861`；cache token hit ≈ **99.67%** → TPM 偏乐观。

| 并发 | 成功 | fail | success_rate | concurrency_mean | RPM mean | RPM peak(60s) | lat p50 | lat p90 | TTFT p50 | content_rate | wall_s |
|---|---|---|---|---|---|---|---|---|---|---|---|---|
| **400** | 4854 | 2 | **0.9996** | 387.7 | **916.4** | **1226** | 26.0s | 29.9s | 20.1s | 0.78 | 318 |
| **800** | 6058 | 0 | **1.00** | 721.4 | **1086.4** | **1651** | **35.9s** | **42.6s** | **27.9s** | 0.77 | 335 |
| **1200** | 6310 | **24993** | **0.2016** | 974.6 | 1099.0† | 1608† | 44.5s | 55.0s | 35.9s | 0.16 | 344 |

† 1200 吞吐被大量本地失败污染，**不能**当容量锚点；成功请求的延迟仍继续变差。

operating point：**800**（runner exit **0**，因 1200 `< success_min` 停在该点）。

### 3.1 1200 失败直方图（根因）

| status / 类 | 次数 | 含义 |
|---|---|---|
| **0 / Errno 24 Too many open files** | **24631** | client soft **`ulimit -n=1024`** 打满 |
| 0 / SSL CERTIFICATE_VERIFY_FAILED | 319 | FD 耗尽后的连带噪声 |
| **503 overloaded_error** | **28** | 网关在途闸门/过载 shed（占比极小） |
| 0 / Errno 11 EAGAIN | 11 | 本地资源瞬态 |
| 200 upstream_stream_read_error | 4 | 上游流中断 |

压测机进程 limits：`Max open files soft=1024 / hard=1048576`。  
1200 档 `concurrency_peak` 仅 **1152**（达不到标称 1200），与 FD 耗尽一致。

**因此：本轮「1200 塌了」首先证明的是 client 文件描述符预算不够，不是「网关在 1200 必挂」。**

### 3.2 亚线性（400 → 800，排除 client 4C 假说）

| 比值 | 本轮 8C client | 同日先验 4C client（800 报告） |
|---|---|---|
| 目标并发 | ×2 | ×2 |
| concurrency_mean | ×1.86（388→721） | ×2.26（299→676） |
| RPM mean | **×1.19**（916→1086） | **×1.25**（870→1088） |
| latency p50 | ×1.38（26→36s） | ×1.93（18→34s） |
| TTFT p50 | ×1.39（20→28s） | ×1.83（13→24s） |

Little’s Law 粗检：`RPM ≈ C_mean / lat_p50 × 60`  
→ 400：≈895；800：≈1205（量级贴合实测 mean）。  
**并发上去后单请求变慢，吞吐无法线性跟上。** 升 client 核数后 800 RPM mean 与 4C 轮几乎相同（~1086 vs ~1088），说明 **亚线性主因在服务侧延迟路径，不在压测机 CPU**。

1200 档即便筛掉本地失败，成功侧 lat/TTFT 继续抬（p50 44s / 36s），RPM mean 仍贴在 ~1100——**吞吐平台区约在 800–1100 RPM mean**。

### 3.3 压测机资源

| 指标 | 值 |
|---|---|
| nproc | 8 |
| soft `ulimit -n` | **1024**（本轮硬伤） |
| loadavg1 max / p95 / mean | **14.62** / 9.55 / 5.34 |
| MemAvailable min | ≈12.8 GiB（总 ~16 GiB，未 OOM） |

8C 下 load 仍偏高（末段含 FD 风暴重试），但内存充足。下一轮 ≥1000 并发必须先把 soft nofile 提到远高于 `2×concurrency`（例如 ≥65535），否则会再次误判网关。

---

## 4. 服务侧采样（压测中 lite）

| 节点 | 通道 | load1 max | 容器 CPU% max† | 内存占用 max | 其他 |
|---|---|---|---|---|---|
| app-a | SSM lite | 2.96 | **329.7** | ~2.0 / 3 GiB | 事后回落到低负载 |
| app-b | TAT lite | **4.29** | **387.7** | ~2.0 / 3 GiB | 同规格更热 |
| data | SSM lite | **22.33**（p95 17.3） | PG docker **189.9** | PG ~1.6 / 3 GiB | **pg_total/active max 172 / 162** |

† `docker stats` 多核百分比，可 >100。

解读：

- 双应用在高压窗 **CPU 接近打满**，与延迟抬升一致 → **应用算力/请求路径是软瓶颈**。  
- 数据机短时极热、PG 连接冲到 ~170，相对 `max_connections=400` 与应用池 80×2 **未顶死**，但说明库侧已进入可观察压力区。  
- 网关 `503` 仅 28 次 → **512 闸门在 1:1 下对「能真正发出的请求」几乎未先爆**；1200 本地发不出的请求不会打到闸门。  
- 容器内存 ~2GiB/3GiB，接近但未 OOM。

跑中 / 跑后公网 `/health=200`；跑后 app/data 连接回落到个位数～二十余。

---

## 5. 瓶颈对照（更新）

| 层级 | 状态 | 依据 |
|---|---|---|
| 入口 / 可用性 | **扛住** | 中测与事后 health 200；无整站宕机 |
| Client FD / ulimit | **1200 硬伤** | 24631× Errno 24；soft 1024 |
| Client CPU（8C） | 偏热但非 800 亚线性主因 | 8C 与 4C 的 800 RPM mean 几乎相同 |
| 应用延迟 / CPU | **软瓶颈（亚线性主因）** | lat/TTFT↑；app CPU% 打满量级 |
| `GATEWAY_MAX_IN_FLIGHT=512` | 本轮几乎未暴露 | 仅 28×503；大量请求未出 client |
| PG 连接（80×2 / max 400） | **有压力、未打满** | peak 172/162 |
| 账号席位 | 未暴露为失败模式 | runner 用 pool-slots 2000；无席位耗尽主导失败 |
| 容器内存 3GiB | 逼近观察点 | peak ~2.0GiB |

---

## 6. 与先验对照

| 问题 | 本轮答案 |
|---|---|
| 升 8C client 能否消除亚线性？ | **否**。800 RPM mean 仍 ~1086，与 4C 轮同级。 |
| 1200 是否说明网关容量到顶？ | **否（本轮）**。失败主因是 client nofile；需抬 ulimit 后重测才能谈服务硬顶。 |
| 正式容量锚点？ | 仍记 **800**；RPM peak≈1651（本轮）/1773（4C 轮）；TPM 高 cache 须标注乐观。 |
| 数据机是否危险？ | 高压窗 load/PG CPU 高，但 max_connections 与服务可用性未破；盯盘阈值应覆盖该区。 |

---

## 7. 下一步建议

1. **容量叙事**：对外仍以 **800 正式可过** 为上沿；不要引用 1200 成功率。  
2. **若要重测 ≥1000**：  
   - 压测机 `ulimit -n` soft≥65535（并写入 runner）；  
   - 继续双应用 + data 压测中采样（app-b 走 TAT，勿只靠 SSM）；  
   - 观察单机 in-flight 是否贴 512——若贴，优先第 3 应用机，而非只加 client。  
3. **若要抬吞吐平台**：重点在 **降低 lat/TTFT 或加应用算力水平扩展**，不是继续堆 client 核数。  
4. **运维**：压测机 `expire_after=2026-11-19` 到期前销毁；远程 key 保持不落盘。

---

## 8. 相关路径

- 800 报告：`docs/ops/reports/cn-prod-deepseek-flash-pool-perf-800-20261117.md`
- 200/400 先验：`docs/ops/reports/cn-prod-deepseek-flash-pool-perf-20261117.md`
- 摘要 / client / 服务峰值：`docs/ops/reports/artifacts/cn-prod-deepseek-flash-pool-perf-1200-20261117-*.json`
- 配方：`ops/observability/tpm_burst_recipe1.py`
- 决策：`docs/decisions/decided.md`（2026-11-17 client 升 8C + 1200）
