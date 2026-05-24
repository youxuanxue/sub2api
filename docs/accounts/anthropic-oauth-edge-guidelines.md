# Anthropic OAuth Edge（TokenKey）：TLS 与 tier 基线

面向 **Stage0 Edge** 上 Anthropic、`type=oauth` 账号的**现行运维约定**。  
数值型 tier 字段（RPM、会话、并发、sticky、窗口成本等）的**单一真值源**为：

- `deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`

对该 JSON 的现场写入、校验与级联：**`tokenkey-anthropic-oauth-config`** skill → `ops/anthropic/manage-anthropic-config.py`（snapshot → check → plan → apply → verify），写入面 **(A)**。

---

## Canonical TLS fingerprint

| 维度 | TokenKey 要求 |
|---|---|
| **`tls_fingerprint_profiles.name`** | **`claude_cli_2_1_142_node24_20260515`** |
| Profile 字段体（cipher、extensions、curves…） | 与 tier baseline JSON **`shared_baseline.tls_profile`** 一致 |
| 账号 **`accounts.extra`** | `enable_tls_fingerprint=true`，且 **`tls_fingerprint_profile_id`** 指向上述模板对应的 DB 主键 |

`(A)` 的 **`generate_sql`** 会 **`INSERT … ON CONFLICT (name)`** upsert canonical 模板，并把 `accounts.extra.tls_fingerprint_profile_id` 写成刚插入行的 `id`。

可作字段对照的镜像 JSON：`deploy/aws/stage0/claude_cli_2_1_142_node24_20260515.json`。

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
