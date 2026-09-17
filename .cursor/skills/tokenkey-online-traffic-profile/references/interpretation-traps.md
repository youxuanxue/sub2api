## 0) 为什么必须"逐分钟重建"，不能只看 gauge

admin 账号卡片四个数字是**瞬时 gauge**，主要读 Redis，**没有逐分钟历史**：

| 卡片 gauge | Redis 落地 key | 历史保留 |
|---|---|---|
| 🎛 并发 `cur/concurrency` | `concurrency:account:{id}`（zset，活跃 slot） | ❌ 仅当前；`wait:account:{id}` 为等待槽 |
| 📊 上游窗口利用率 5h/7d | `accounts.extra` 被动采样（`session_window_utilization` / `passive_usage_7d_utilization`） | ❌ 仅当前快照；调度用 global 0.98/0.02 soft gate（非 tier 美元 cap） |
| 👥 会话 `cur/max_sessions` | `session_limit:account:{id}`（zset，按 `session_idle_timeout_minutes` 过期，默认 5、可被 extra 覆盖） | ❌ 仅当前 |
| 🕐 RPM `cur/base_rpm [T]` | `rpm:{id}:{unixMinute}`，**TTL=120s** | ❌ 只留最近 ~2 分钟 |

**结论**：除“当前快照”外，过去 N 小时的逐分钟值只能从 **access log（`http request completed`）+ `sticky.scheduler_entry` + `usage_logs`** 重建。这是本 skill 的核心。

**已踩过的坑**：
1. `grep -c '429'`/`'529'` 是**误报**——会命中 UUID、`body_bytes`、`latency_ms` 里的子串。判真实上游限流/过载要解析 JSON 或匹配 `rate_limit_error`/`overloaded_error`，不要数裸数字。
2. 瞬时 gauge ≤ cap **不代表**历史没触顶（峰值已过、配置事后被改）。务必重建；并确认 cap 在事发时段的取值（如 `max_sessions` 被从 16 改到 30）。
3. account 被判不可用/`no available accounts` 时三个本地 cap（concurrency/max_sessions/base_rpm）都能触发且**不留专门日志**，prod Debug 级 `sticky.layer*` 默认关——只有重建数据能区分。
4. **不要先认定 base_rpm**（本 skill 第一版排障就误判过）。判别口诀：
   - `no available` 那一分钟若 **RPM<base_rpm 且 conc<concurrency**（低负载也 503）→ 几乎一定是 **session 面**：算 `全局活跃会话 vs Σ(max_sessions)`。
   - 现象是「**粘性请求 200、非粘性(新会话/sticky miss)503**」→ 黄区 RPM **或** session 满二选一；用 RPM 序列区分：RPM≥base 选黄区，RPM<base 选 session。
   - 只有某分钟 RPM 真的 ≥base_rpm 才轮到 base_rpm 黄/红区。
5. **重建出的 `activeSess` 是上界，不是触顶证据**（与坑 2 对称）。§3 用 `IDLE_MIN` 尾随窗按 `session_hash` 去重计活跃会话，这个窗通常**比真实 zset 的过期行为更宽**，所以 `activeSess` 常会**高于**当下 `ZCARD session_limit:account:*` 之和，甚至越过 Σ(max_sessions)。**单看 `activeSess>Σmax` 不能判 session 触顶**——必须同时满足「该时段确有 503 / `no available`」且「现象是粘性 200、非粘性失败」。零 503 时 `activeSess` 越线只说明会话维度余量最小、值得盯，**不是**已触顶。核对方式：对照当前 `ZCARD` 之和（live 真值）与该时段真实失败计数。
6. **字段来源混淆 + 数列号陷阱**（2026-05-23 现场踩坑）。accounts 表里 cap 字段一半是**顶层列**（`concurrency / schedulable / rate_limited_at / rate_limit_reset_at / overload_until / temp_unschedulable_until / temp_unschedulable_reason / session_window_*` / `error_message`），一半在 `extra` JSON（`base_rpm / rpm_strategy / rpm_sticky_buffer / max_sessions / session_idle_timeout_minutes / stability_tier`）—— 同名字段 `extra->>'concurrency'` 会查到 NULL，必须用 `accounts.concurrency`。**更危险的失败模式**：`psql -t -A -F'|'` 把 20+ 列输出为纯位置 `|` 分隔串、无列头，肉眼数列号几乎必错（曾把不同 extra 键当成相邻列，结论从"session 触顶"翻成"上游 503"）。**硬纪律**：本 skill 所有 cap / 不可调度证据查询**强制用 §1 给出的 `row_to_json` 固化 SQL**，输出形如 `{"id":4,"max_sessions":"100","base_rpm":"28",...}`，字段名跟值粘在一起、物理不可能错列；禁止自由写多列管道 SELECT。下游展示也只能 key=value，禁止"a4: 28/20/100/8/l5" 这种靠列位读的自由文本。
7. **链式失败 / 镜像账号**。prod 上以 `cc-<edge>-oauth`（如 `cc-us1-oauth` / `cc-uk1-oauth`）命名的 anthropic Key 账号，其 credentials 上游就指向对应 edge 域名（`api-<edge>.tokenkey.dev`）。edge 端任何 5xx / `no available accounts` 都会作为 **upstream 503** 透传回 prod；prod 路由层的 `anthropic_upstream_error` 关键词阈值规则会基于这些 transient 503 累计计数，达阈值（默认 3/3）后给该 prod 账号写 `temp_unschedulable_until`（tier-based cooldown，常见 10m），admin UI 即显示「临时不可调度」黄标。**归因纪律**：看到 prod `temp_unschedulable_reason.matched_keyword='anthropic_upstream_error'` 时，**真因在 edge 同时段画像**，不是 prod 本地 cap；必须切到对应 edge 跑一遍 §1+§3 才算定案。把 prod 的 cooldown 当根因 = 漏判 edge 容量问题。
