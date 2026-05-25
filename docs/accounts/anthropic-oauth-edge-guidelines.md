# Anthropic OAuth Edge（TokenKey）：TLS 与 tier 基线

面向 **Stage0 Edge** 上 Anthropic、`type=oauth` 账号的**现行运维约定**。  
数值型 tier 字段（RPM、会话、并发、sticky、窗口成本等）的**单一真值源**为：

- `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`

对该 JSON 的现场写入、校验与级联：**`tokenkey-anthropic-oauth-config`** skill → `ops/anthropic/manage-anthropic-config.py`（snapshot → check → plan → apply → verify），写入面 **(A)**。

---

## Canonical TLS fingerprint

| 维度 | TokenKey 要求 |
|---|---|
| **`tls_fingerprint_profiles.name`** | **`claude_cli_2_1_150_node24_20260525`** |
| Profile 字段体（cipher、extensions、curves…） | 与 tier baseline JSON **`shared_baseline.tls_profile`** 一致 |
| 账号 **`accounts.extra`** | `enable_tls_fingerprint=true`，且 **`tls_fingerprint_profile_id`** 指向上述模板对应的 DB 主键 |

`(A)` 的 **`generate_sql`** 会 **`INSERT … ON CONFLICT (name)`** upsert canonical 模板，并把 `accounts.extra.tls_fingerprint_profile_id` 写成刚插入行的 `id`。

可作字段对照的镜像 JSON：`deploy/aws/stage0/claude_cli_2_1_150_node24_20260525.json`。

**HTTP 层（User-Agent / `x-stainless-*`）**：TLS 模板不决定出站 HTTP 指纹。账号绑定上述 canonical 模板时，网关会把 Redis `fingerprint:{accountID}` 的 HTTP 字段钉死在同一 JSON 的 `observed.*`（与 TLS 参数独立）；prod→edge 透传的多版本 ingress UA 不会改写上游 UA。`ops_error_logs.user_agent` 仍是入口侧客户端 UA，不是 `api.anthropic.com` 所见值。

---

## Canonical 路径上的客户端身份要求

账号绑定 canonical TLS 模板后，**网关会拒绝来自明显的第三方 SDK / 通用 HTTP 客户端的 ingress 请求**，返回 HTTP `403 permission_error`，不进 failover、不打 Anthropic 上游。判定基于 ingress `User-Agent` 子串（大小写无关）。

- **拒绝**：`OpenAI/Python`、`openai-python`、`httpx/`、`python-requests/`、`node-fetch`、`axios/`、`got (`、`undici`、`go-http-client`、`curl/`、`wget/`、`postman`、`insomnia`、`libcurl`、`okhttp`、`java/`、`reqwest/`、`aiohttp`
- **放行**：`claude-cli/*`、`claude-code/*`、空 UA、未列入拒绝表的未知 UA（deny-list-only 策略——未来 cc 新变体无需改代码）

权威列表：`backend/internal/service/gateway_service_tk_canonical_oauth_guard.go` 的 `canonicalIngressUAForbiddenSubstrings`。需要追加新条目时直接在该 slice 末尾添加（**append-only**），并同步单测 + sentinel。

**理由**：canonical 账号对应的是 Anthropic 个人 Claude Code subscription，被第三方 SDK 大流量长时间使用会让 Anthropic 风控聚合识别为「订阅账号被批处理代理共享」——2026-05-25 edge-uk1 账号 `EN-LD-EC2-16-3` 的 hold 事件就是这个 cohort signal。需要绕过这层（如内部脚本 / OpenAI SDK 路径）：用 API-key 类账号或非 canonical TLS 的 OAuth 账号；不要让非 cc 客户端共用 cc subscription。

---

## Canonical 路径上的退役 opus 重映射

canonical 路径上请求**已退役的 opus 模型**（`claude-opus-4-0` ~ `claude-opus-4-5*` ~ `claude-opus-4-6*`，含带日期后缀如 `claude-opus-4-6-20250930`）会**自动重映射为当前默认 `claude-opus-4-7`**。Sonnet 全系列、Haiku 全系列、当前默认 `claude-opus-4-7` 不动。

行为细节：

- **客户端透明**：流式 / 非流响应里的 `model` 字段会被改回客户端发的原始 id（依赖既有 `originalModel` ↔ `mappedModel` 框架；见 `gateway_service.go` 的 `handleStreamingResponse` / `handleNonStreamingResponse`）。
- **本地 telemetry / billing 仍按 `originalModel` 记账**——`usage` / `ops_error_logs.requested_model` 看到的是客户端请求 id（4-6），不是重映射结果（4-7）。
- **上游 Anthropic 实际看到 `claude-opus-4-7`**——这是收敛目的。

权威列表：`backend/internal/service/gateway_service_tk_canonical_oauth_guard.go` 的 `canonicalDeprecatedOpusPrefixes` + `canonicalDefaultOpus`。

**理由**：真实 `claude-cli/2.1.150` 默认只发 `claude-opus-4-7` / `claude-sonnet-4-6` / `claude-haiku-4-5-*`；同账号上同时出现 4-6 + 4-7 是「混合发行版客户端共享同一个 OAuth」的强 cohort signal——风控会聚合识别。Sonnet 4-5/4-6 与 Haiku 4-5 在真实 cc 客户端中均处于活跃状态，**仅 opus 退役系列**会被改写。

---

## 反模式（避免出现 silent 漂移）

1. **只启用 TLS、无 DB 模板 / 无 `tls_fingerprint_profile_id`**：运行时会退回**内置默认** ClientHello；运维侧无法在模板库里点名当前用的是哪一套参数。
2. **`tls_fingerprint_profile_id = -1`（随机模板）**：库里每多一条 **`tls_fingerprint_profiles`**，随机抽到其中任意一条的几率就上升——Handshake **不可稳定复现**。生产 OAuth 链路不应依赖随机模板。
3. **多套手建模板与 canonical 并存**：除随机路径（第 2 条）外，多套件易误选旧名。**已无账号绑定**的行从 Admin 「TLS 指纹模板」删除，只保留 canonical 一条。**已废止名** **`claude_cli_nodejs24_fixed`** 禁止新绑定；删行前确认无主账号其 **`extra.tls_fingerprint_profile_id`** 指向该行 id。

---

## 延伸阅读（自动化入口）

| 载体 | 说明 |
|---|---|
| `.cursor/skills/tokenkey-anthropic-oauth-config/SKILL.md` | 五条流水线、三条写入面、故障表与附录的真值索引 |
| `ops/anthropic/manage-anthropic-config.py` | orchestrator |
| `ops/anthropic/check-edge-oauth-stability.py` | tier drift guard + **`generate_sql`** |
| `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json` | tier + TLS profile 写入值来源 |
