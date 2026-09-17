## 1. TLS fingerprint canonical 模板（跨 edge 对齐）

现行约定见 [`docs/accounts/anthropic-oauth-edge-guidelines.md`](../../../../docs/accounts/anthropic-oauth-edge-guidelines.md)。

**反模式**：`enable_tls_fingerprint=true` 但 **`tls_fingerprint_profiles` 无对应模板行／账号无可靠 `tls_fingerprint_profile_id` 绑定** → 运行时会退回**内置默认** ClientHello，后台无法在模板表中点名在用参数；**不要用** **`tls_fingerprint_profile_id=-1`** 随机指纹跑生产 OAuth（库里每多一条模板，随机抽到其中一条的不确定性就上升）。

**标准要求**：每一条 **Anthropic、`type=oauth` 的边缘账号**，必须绑定 **`tls_fingerprint_profiles.name = tk_canonical_cc_oauth`**；字段体以 **`deploy/aws/stage0/anthropic-oauth-stability-baselines-tiered.json`** 的 `shared_baseline.tls_profile` 为单一真值源（对照：`deploy/aws/stage0/tk_canonical_cc_oauth.json`）。

**与本流水线的关系**：guard-drift force-template-rewrite（`generate_sql`）会 **`ON CONFLICT (name)` upsert** canonical profile，并把 `accounts.extra.tls_fingerprint_profile_id` 写成对应行 **`id`**；`check` / `verify` 比对 live `tls_profile.*` vs baseline **`tls_profile`** 块。**弃用手建并排模板**：**已废止名** **`claude_cli_nodejs24_fixed`** 不得再绑定新账号；库中无主账号绑定其 id 时，须在 Admin「TLS 指纹模板」删除该行。**删前**须确认无主账号 **`extra.tls_fingerprint_profile_id`** 仍指向该 id，否则运行时查找不到行→退回内置默认（silent 漂移）。

**admin UI**：`enable_tls_fingerprint` + 下拉选 canonical 名一致；尚无模板行时先跑一次 `remediate-guard-drift`，再绑定账号。

> ⚠️ TLS 模板的 upsert+绑定 SQL 历史上挂在写入面 (A) `edge_account_tier` apply 里（与 tier 数值同事务）。现在 tier **数值**由 admin UI ApplyTier 写、reconciler 自愈并发；本 skill 通过 **`plan-guard-drift-fix` / `remediate-guard-drift`** 只触发 force-template-rewrite 的那一段 SQL（重写 TLS profile + 绑定 + credentials 模板字段），**不**把 tier baseline 数值当作可调旋钮。
