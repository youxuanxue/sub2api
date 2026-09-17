## 4) 解读规则（哪个参数触顶）

| 观察 | 判定 |
|---|---|
| 某分钟 `rpm ≥ base_rpm` 且非粘性请求失败/`no available` | **base_rpm 黄/红区**：非粘性被 RPM 闸挤出。 |
| `peak_conc ≈ concurrency` 且新请求 429/排队超时 | **concurrency 触顶**（`Concurrency limit exceeded` 或 `umq`/wait 超时）。 |
| `global activeSess > Σ(max_sessions)` **且该时段确有 503/`no available`** 且**粘性 200、非粘性失败** | **max_sessions 触顶**：新会话被 `checkAndRegisterSession` 拒（`ErrNoAvailableAccounts`，gateway_service.go ~line 2121），已绑定会话 `ZSCORE` 命中放行。 |
| `activeSess > Σ(max_sessions)` **但零 503**（全程 200） | **未触顶**：`activeSess` 是 IDLE 窗上界、高于 live `ZCARD` 之和（见 §0 坑 5）。结论=会话维度余量最小、值得盯，**不是**触顶；核对当前 `ZCARD` 之和与真实失败计数。 |
| RPM<base、conc<max、sess 未饱和，却仍 503 | 查上游：解析 JSON 找 `rate_limit_error`/`overloaded_error`/`cooldown`/`rate_limit_reset_at`，**别数裸 429/529**。 |
| `nonStickyRpm` 高、`activeSess` 接近 Σmax | 单 CLI 派生大量短会话 → 会话面先到顶（典型：edge 仅 2 账号时）。 |
| prod 账号 `temp_unschedulable_until > now()` 且 `temp_unschedulable_reason.matched_keyword='anthropic_upstream_error'` | **链式失败**：prod 把 edge 透传回来的 503 累计到本地阈值规则，自动 cooldown。**这不是根因**——切到对应 edge 跑 §1+§3 找 edge 实因（max_sessions / concurrency / 真上游 503）。归因责任在 edge 同时段画像。 |

判 session 饱和的关键不等式：**全局活跃会话 > Σ(max_sessions over 可调度账号)** ⇒ 必有新会话落空。务必用**事发时段**的 `max_sessions`（可能被事后调过）。

### 4.1 链式失败 / 镜像账号识别

prod 上 anthropic Key 账号若命名形如 `cc-<edge>-oauth`（如 `cc-us1-oauth` → `api-us1.tokenkey.dev`），归因路径必须是双跳：

```
[edge 实因: max_sessions / concurrency / base_rpm / 真上游 503]
       │
       ▼ 503 透传 (upstream_status_code=503, body="no available accounts" 等)
[prod 路由层 anthropic_upstream_error 关键词阈值规则: 累计 N/N]
       │
       ▼ 写 accounts.temp_unschedulable_until = now()+cooldown(tier-based, 常见 10m)
[admin UI: 临时不可调度黄标]
```

确认是否镜像账号：`SELECT credentials->>'base_url' FROM accounts WHERE id=<prod_acct_id>;`（或 `credentials->>'endpoint'`，依字段名而定）。base_url 指向 `api-<edge>.tokenkey.dev/*` 即镜像。

操作上：先在 prod 跑一次 §1 拿 `temp_unschedulable_reason`，从 `triggered_at_unix` 反推 edge 上的事发分钟（同一秒精度），再到 edge 跑 §1+§3，对照那几分钟的 `actSess / sRPM / nonStk / ZCARD-now / max_sessions / concurrency` 才能定真因。

**机械落点**：这套"切到 edge 逐分钟画像"的双跳，先用 `ops/observability/scan-edge-health.sh [--edges <id>] [--since <N>h]` 一眼定位哪个 edge `verdict=down/degraded`（基于 edge **自身** `served_200 : no_available_429` 比 + 可调度账号数，**不被 prod upstream-429 污染**——prod 那个数对死/活 edge 都显示 ~1300，2026-06-06 压测中 edge-us5 实发 77 个 429、prod 记 1266，详见 troubleshooting skill §0 trap 9），再对 verdict 异常的 edge 跑 §1+§3 逐分钟坐实。**不要**用 prod 的 `upstream-429 by account` 或 `recovered-200` 推断 edge 死活（`recovered-200` 越高反而 = edge 越死、全靠 failover 救）。
