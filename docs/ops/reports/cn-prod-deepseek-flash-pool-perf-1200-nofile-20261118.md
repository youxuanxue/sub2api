# cn-prod deepseek-flash 池压测报告（1200 / 抬高 client nofile 复测，2026-11-18）

## 摘要

针对 2026-11-17 1200 档主因 `Errno 24 Too many open files`（soft `ulimit -n=1024`），
将压测机 soft/hard nofile 提到 **65535** 后，重跑阶梯 `400 → 800 → 1200`。

| 项 | 值 |
|---|---|
| 日期 | 2026-11-18（BJT 约 08:15–08:32） |
| 站点 | cn-prod |
| 模型 | `deepseek-flash` Recipe-1（~10k/1k） |
| Client | `ins-7ltl2zv3` 8C16G；**nofile soft=65535** |
| 阶梯 | `400,800,1200`，hold 300s，`success_min=0.98`，`--pool-slots 2000` |
| 远程产物 | `/tmp/tpm-recipe1/cn-prod-loadtest-1200-nofile-20261118-081536/` |
| 对照 | `…perf-1200-20261117.md`（同阶梯、nofile=1024） |

**结论一句话：** nofile 修好后 **1200 能真正打满 concurrency_peak=1200**；失败从 FD 耗尽变成 **455×503 `overloaded_error`**（成功率 **0.9159** &lt; 0.98）。operating point 仍是 **800**。说明此前 1200「塌了」是 client 伪影；**服务侧硬暴露的是在途闸门/过载 shed + 延迟崩坏**。

---

## 结果

| 并发 | success_rate | fail | RPM mean | RPM peak | lat p50 | TTFT p50 | conc_mean / peak |
|---|---|---|---|---|---|---|---|
| **400** | **1.00** | 0 | 980 | 1289 | 24.5s | 18.4s | 387 / 400 |
| **800** | **1.00** | 0 | **1211** | **1695** | 32.2s | 24.2s | 717 / 800 |
| **1200** | **0.9159** | **456** | 827† | 1249† | **74.9s** | **69.4s** | 1082 / **1200** |

† 含 503 失败，吞吐不可作容量锚点。

1200 失败直方图：`503 overloaded_error` **455**；另有 1 次 `gateway_concurrency_limit`（账号并发限制）夹在 200/错误样本中。

| 对照 | 2026-11-17（nofile=1024） | 本轮（65535） |
|---|---|---|
| 1200 concurrency_peak | 1152（发不满） | **1200** |
| 1200 主失败 | Errno 24 ×24631 | **503 ×455** |
| 1200 success_rate | 0.20 | **0.92** |
| 800 RPM mean | ~1086 | **~1211** |

---

## 服务侧（lite）

| 节点 | load1 max | 容器 CPU% max |
|---|---|---|
| app-a | 4.07 | **393** |
| app-b | 4.85 | **390** |
| data | **23.5** | PG docker **192** |

Client loadavg1 max **19.6**（8C，偏热但未再炸 FD）。公网 `/health` 跑后 **200**；远程 key 已 scrub。

---

## 含义

1. **正式上沿仍是 800**（本轮 800 仍 100%，且 RPM mean 略好于昨日）。  
2. **1200 已碰到服务硬行为**：双机 `GATEWAY_MAX_IN_FLIGHT=512` 合计 1024，1200 在途必然 shed → 503；同时 lat/TTFT 翻倍以上。  
3. 下一档扩容应优先 **加应用机或审慎上调闸门+内存**，而不是继续堆 client。
