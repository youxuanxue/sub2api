## 9. 扩展阅读

- `ops/anthropic/manage-anthropic-config.py`（orchestrator；本 skill 用其 `snapshot` / `check` / `plan-guard-drift-fix` / `remediate-guard-drift` / `apply` / `sync-runtime` / `plan-http-mimicry-sync` / `verify` / `apply-tiers-live`）
- `backend/internal/service/tier_service.go`（`ensureSeededFromBaseline` UpsertByName + `invalidateAndNotify`：§2.5 `apply-tiers-live` 复刻的缓存失效真值源）
- `backend/internal/service/anthropic_config_reconciler.go`（**§3 写入面 A 并发 / B / C / E 的 per-node 自愈真值源**；文件头列出 boundary 与 step 顺序）
- `backend/internal/handler/admin/account_handler_tk_tier.go` + `backend/internal/service/tier_service.go`（admin UI `ApplyTier`：tier 数值的写入入口）
- `backend/internal/handler/admin/group_handler.go`（group `claude_code_only` 写入入口）
- `backend/internal/service/anthropic_operator_concurrency.go`（控制面与 reconciler 共享的 Σ schedulable→`users.id=1` 语义）
- `ops/anthropic/check-edge-oauth-stability.py`（`generate_sql`：`tls_profile` upsert + `extra.tls_fingerprint_profile_id` 绑定）
- `docs/accounts/anthropic-oauth-edge-guidelines.md`（OAuth edge TLS + UA 现行约定短文）
- `deploy/aws/stage0/tk_canonical_cc_oauth.json`（canonical TLS profile JSON，与 tiered baseline `shared_baseline.tls_profile` 对齐）
- `deploy/aws/stage0/anthropic-http-mimicry-baselines.json`（HTTP UA / mimicry manifest 唯一真值源）
- `ops/observability/probe-account-emails.sh`（fleet OAuth email + normalize 开关只读审计；§8.1）
- `ops/observability/apply-account-contact-email.sh`（fleet OAuth email 写入；§8.2）
