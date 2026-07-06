# spec-delta: canonical UA suffix sdk-cli → cli (REPL cohort)

## Background

Prod ingress on shared Claude Code OAuth accounts is dominated by interactive REPL traffic: `claude-cli/<version> (external, cli)` with system banner `You are Claude Code, Anthropic's official CLI for Claude.` The prior canonical egress pin used `(external, sdk-cli)`, which matches non-interactive `-p` / Agent SDK subrequests — not the prod ingress cohort.

Local capture evidence (CC 2.1.202, `.tls_list/20260706T234304Z-cc-interactive/`):

| Mode | User-Agent | system[0] |
| --- | --- | --- |
| `capture --http` / `-p` | `(external, sdk-cli)` | Agent SDK identity |
| Interactive REPL (PTY + expect) | `(external, cli)` | Claude Code REPL banner |

## Delta

| Area | MODIFIED |
| --- | --- |
| Canonical egress UA suffix | `(external, sdk-cli)` → `(external, cli)` |
| Ops capture | `ops/anthropic/capture-cc-interactive.sh` + `capture_interactive_repl.exp` |
| Smoke defaults | `ops/stage0/smoke_lib.sh` and probes use cli suffix |
| Sentinels | `gateway-tk.json`, `check-cc-version-sync.py` regex anchors |
| mitm addon | also logs `api.tokenkey.dev` for local REPL capture |

| Area | UNCHANGED |
| --- | --- |
| TLS ja3 profile `tk_canonical_cc_oauth` | Same ClientHello across both modes |
| Ingress allow-list | Both cli and sdk-cli ingress still accepted |
| Historical migration SQL snapshots | Left as historical sdk-cli literals |

## Scenarios

1. **正向**: Given canonical OAuth account on edge, when egress fingerprint is built, then User-Agent ends with `(external, cli)`.
2. **负向**: Given interactive capture log with sdk-cli UA, when `validate_interactive_http_log` runs, then validation fails.
3. **回归**: Given ingress `(external, sdk-cli)` on canonical profile, when `GetOrCreateFingerprint` runs, then cached egress UA remains resolver-driven `(external, cli)`.

## Validation

```bash
cd backend && go test -tags=unit ./internal/service/... -run 'Canonical|BuildCanonical'
python3 ops/anthropic/test_capture_cc_interactive.py
python3 scripts/sentinels/check-cc-version-sync.py
bash ops/anthropic/capture-cc-interactive.sh capture
python3 ops/anthropic/capture_cc_fingerprint.py check --bundle .tls_list/20260706T234304Z-cc-interactive.bundle.json
```
