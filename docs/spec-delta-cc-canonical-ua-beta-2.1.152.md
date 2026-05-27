# spec-delta: PR #423 — cc 2.1.152 OAuth mimicry alignment

## Background

PR [#423](https://github.com/youxuanxue/sub2api/pull/423) aligns TokenKey canonical / OAuth Claude Code mimicry with **cc 2.1.152 mitm capture** (cc0 → gost → socks) and us1 Phase 0 cross-checks:

- **TLS ClientHello** unchanged across cc 2.1.142 → 2.1.152 (same ja3); no DB TLS profile migration.
- **HTTP fingerprint** drift was the actionable gap: compile-time UA default, `anthropic-beta` token set, and Haiku mimicry path lagged real cc traffic.

Community / upstream context (why this PR exists, not what it fully solves):

| Source | Takeaway |
|--------|----------|
| [Wei-Shaw/sub2api#580](https://github.com/Wei-Shaw/sub2api/issues/580) | Hard-coded mimic UA causes **version up/down churn** on shared OAuth accounts when real cc and third-party clients mix. |
| [Wei-Shaw/sub2api#766](https://github.com/Wei-Shaw/sub2api/issues/766) | UA rewrite → mimic path + **un-rewritten `metadata.user_id`** → identity mismatch → ban risk. |
| [Wei-Shaw/sub2api#1755](https://github.com/Wei-Shaw/sub2api/issues/1755) | Upstream wants configurable header rewrite; TK uses **code-canonical constants + admin UA setting** instead. |
| Anthropic 2026-04 third-party policy | OAuth subscription plan limits apply to **first-party cc**; third-party harnesses draw **extra usage** even with perfect beta alignment. |
| LINUX DO (deployment / ban threads) | **TLS/HTTP fingerprint mismatch**, IP profile, pooling concurrency, and token refresh batching dominate anecdotal ban stories — beta/UA alone is insufficient. |

Related TK docs: `docs/accounts/anthropic-oauth-edge-guidelines.md`. Prior mimicry work: `docs/spec-delta-2489-580-no-web-impact.md`.

## Delta

### ADDED

- `BetaAdvisorTool`, `BetaAdvancedToolUse`, `BetaCacheDiagnosis` in `backend/internal/pkg/claude/constants.go`.
- `FullClaudeCodeHaikuMimicryBetas()` — 8-token Haiku OAuth set (cc 2.1.152 capture order).
- `JoinBetaHeader()` helper; `backend/internal/pkg/claude/constants_test.go` capture-order regression tests.
- Sentinel anchors in `scripts/sentinels/gateway-tk.json` for the beta registry.

### MODIFIED

- `DefaultClaudeCodeUserAgentVersion`: **2.1.150 → 2.1.152** (`identity_service_tk_canonical_http.go`).
- `CLICurrentVersion` / `DefaultHeaders` / default fingerprint UA: **2.1.92 → 2.1.152** (non-canonical mimic + billing block).
- `FullClaudeCodeMimicryBetas()` Sonnet/Opus set: **10 tokens**, order matches cc 2.1.152; adds advisor / advanced-tool-use / cache-diagnosis.
- `DefaultBetaHeader`, `HaikuBetaHeader`: rebuilt from the new mimicry functions (no `fine-grained-tool-streaming` on OAuth paths).
- OAuth mimic path (`gateway_service.go`): Haiku uses `FullClaudeCodeHaikuMimicryBetas()` instead of `{oauth, interleaved}` only.
- Smoke default UA: `ops/stage0/smoke_lib.sh` → `claude-cli/2.1.152 (external, sdk-cli)`.
- TLS profile snapshot text: `deploy/aws/stage0/tk_canonical_cc_oauth.json`, tier baseline description strings.

### REMOVED (OAuth mimic paths only)

- `fine-grained-tool-streaming-2025-05-14` from **OAuth** default / Sonnet mimicry sets (still present on `APIKeyBetaHeader`).

### NOT changed (explicit non-goals)

- TLS cipher/extension bodies in `tk_canonical_cc_oauth` DB profile.
- `RewriteUserID` / JSON `metadata.user_id` rewrite logic ([#766](https://github.com/Wei-Shaw/sub2api/issues/766)).
- Monotonic global UA cache across all mimic clients ([#580](https://github.com/Wei-Shaw/sub2api/issues/580) full fix).
- `backend/migrations/129_seed_claude_code_template.sql` **「Claude Code 伪装」** channel monitor template (still UA 2.1.114, partial beta) — separate bump if used in prod.

## Scenarios

### Core positive

1. **Canonical OAuth + unset admin UA setting + post-release deploy**  
   Given edge on new binary and empty `claude_code_user_agent_version`, when smoke hits canonical account, then upstream UA is `claude-cli/2.1.152 (external, sdk-cli)` and Sonnet `anthropic-beta` matches 10-token capture set.

2. **Admin UA bump without redeploy**  
   Given live edge still on compile default 2.1.150, when admin PATCH `claude_code_user_agent_version=2.1.152`, then next OAuth forward self-heals Redis canonical UA (no SQL / TLS apply).

3. **Haiku OAuth mimicry**  
   Given non-cc client on OAuth account + Haiku model, when gateway merges mimic betas, then header contains 8-token Haiku set **without** `effort` / `advanced-tool-use`, and body still **does not** auto-inject `context_management` (regression: upstream #2506).

### Core negative

1. **Third-party client policy boundary**  
   Given OpenCode / ChatWise / OpenAI SDK client (not cc), when betas are aligned, upstream may still return **extra usage / third-party** — not a regression from this PR; do not chase with more beta tokens alone.

2. **Ingress UA cohort on same account**  
   Given same OAuth account sees ingress `2.1.150` and `2.1.152` in `usage_logs`, when only compile default changes, then **ingress mix persists** until clients upgrade; canonical **upstream** UA is unified but ingress telemetry still flags mixed clients.

3. **Stale DB monitor template**  
   Given operator uses migration-129 「Claude Code 伪装」 template on channel monitor, when gateway uses PR #423 constants, then monitor requests may still send **2.1.114 + 5-token beta** — drift independent of gateway.

### Post-merge ops checklist

Run after PR #423 merges and on each deployable edge (prod + edge matrix):

| # | Check | How | Pass criteria |
|---|--------|-----|---------------|
| 1 | Admin UA | PATCH `/api/v1/admin/settings` → `claude_code_user_agent_version=2.1.152` on edges still unset | Next canonical forward uses 2.1.152 upstream UA |
| 2 | Smoke UA | `ops/stage0/smoke_lib.sh` default or `TK_SMOKE_CLAUDE_USER_AGENT` | Requests use `(external, sdk-cli)` shape |
| 3 | Upstream beta (Sonnet) | Probe one `/v1/messages` on canonical OAuth account | 10 tokens incl. `advisor-tool`, `advanced-tool-use`, `cache-diagnosis`; **no** `fine-grained-tool-streaming` |
| 4 | Upstream beta (Haiku) | Same on `claude-haiku-4-5-*` | 8-token set; no `effort` / `advanced-tool-use` |
| 5 | Error budget | `ops_error_logs` / alerts for `extra usage`, `third-party apps now draw` | Rate not **worse** than pre-deploy baseline; if up, inspect client mix / IP / concurrency (not beta list) |
| 6 | Ingress cohort | SQL on `usage_logs.user_agent` for canonical accounts (120m window) | Trend toward single cc patch version on ingress; upstream already unified |
| 7 | Template drift | If `channel_monitor_request_templates` 「Claude Code 伪装」 is used | Bump template or disable; do not rely on migration-129 seed alone |
| 8 | Middle proxy | Confirm no layer between cc and TokenKey rewrites UA without rewriting `metadata.user_id` | Avoid #766 pattern |
| 9 | Client env | Users on OAuth direct cc | No stray `ANTHROPIC_API_KEY` / conflicting `ANTHROPIC_AUTH_TOKEN` in shell or `.claude/settings.json` env |

**us1 immediate action (pre-release):** admin `claude_code_user_agent_version=2.1.152` — no redeploy required.

**Known remaining gaps (track separately, not #423 blockers):**

- Global monotonic UA cache for non-canonical mimic ([#580](https://github.com/Wei-Shaw/sub2api/issues/580)).
- `#766`-class user_id rewrite when ingress UA is stripped by middle boxes.
- Migration-129 monitor template version bump.
- cc patch release cadence (~days): expect next bump via admin setting first, then compile default on following release.

## Validation

### Automated (CI / preflight)

```bash
go test -tags=unit ./internal/pkg/claude/... -run 'TestFullClaudeCode'
go test -tags=unit ./internal/service/... -run 'TestFullClaudeCode|TestGatewayService_getBetaHeader|TestNormalizeClaudeOAuthRequestBody_Haiku'
python3 scripts/sentinels/check-gateway-tk.py   # or full ./scripts/preflight.sh
```

### Manual (post-deploy, per edge)

1. Admin PATCH UA setting (checklist row 1).
2. One Sonnet + one Haiku canonical OAuth request; inspect debug / upstream snapshot if enabled (rows 3–4).
3. Compare `extra usage` error rate 24h before vs after (row 5).

### Evidence pointers

- cc0 mitm capture session (2026-05-27): Sonnet/Opus 10-token beta list; Haiku 8-token list; TLS ClientHello unchanged vs 2.1.142 profile.
- us1 Phase 0: unset admin setting → compile default 2.1.150; ingress mix 2.1.150 / 2.1.152 on same account.
