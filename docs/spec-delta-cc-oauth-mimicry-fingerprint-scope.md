# spec-delta: OAuth mimicry fingerprint scope (headers + system, not UA-only)

## Background

Prod 24h probes (2026-07-09) on the mirror chain `OpenAI/Python 2.x → prod anthropic
apikey passthrough → edge anthropic oauth` showed:

- **edge-us3 ingress**: 641/800 anthropic oauth rows carried `OpenAI/Python` UA; 100%
  bound `tk_canonical_cc_oauth` TLS.
- **egress logs (pre-change)**: no `ClaudeMimicDebug` / `UPSTREAM_FORWARD` samples in
  docker window — mimicry correctness was code-path knowledge only, not observable at
  runtime without debug flags.

Community + code already treat **billing system block**, `anthropic-beta`, and
`x-stainless-*` as load-bearing (sub2api#392, #3065, dario#7). Treating "fingerprint"
as **User-Agent version alone** understates the mimicry contract and misleads operators
reading `usage_logs.user_agent` (ingress only).

## Delta

### ADDED

- Structured prod/edge log `gateway.anthropic_oauth_mimic_egress` on OAuth mimic path:
  ingress UA class, egress `User-Agent`, `anthropic-beta`, `x-stainless-*`, system
  billing/identity anchors. **Always** emitted for `OpenAI/Python` / other SDK ingress;
  ~1% baseline sample for CC-native ingress.
- Read-only probe `ops/observability/probe-oauth-mimicry-chain.sh` (+ unit test) to
  correlate ingress SDK UA with egress mimic fields via SSM.
- Fingerprint scope table in `docs/accounts/anthropic-oauth-edge-guidelines.md`.

### MODIFIED

- **Fingerprint definition** (docs + skill): CC OAuth mimicry fingerprint = **TLS JA3**
  + **HTTP headers** (UA, beta set, stainless runtime/pkg, x-app) + **system prompt
  surface** (billing block, identity anchor, geo-stego classes) — UA version sync is
  one sub-field, not the whole contract.
- Sentinel `gateway-tk.json` anchors for `tkMaybeLogOAuthMimicEgressFingerprint`.

### NOT changed

- Kiro oauth path (no Anthropic OAuth mimicry).
- Canonical ingress deny-list for `OpenAI/Python` on `cc_only=true` / strict mode.
- `check-cc-version-sync.py` scope (still UA version propagation only).

## Scenarios

### Core positive

1. Given edge oauth account + `OpenAI/Python` ingress on `/v1/messages`, when
   `shouldMimicClaudeCode=true`, then upstream request logs
   `gateway.anthropic_oauth_mimic_egress` with `ingress_ua_class=openai_python_sdk`,
   `egress_user_agent` matching `claude-cli/… (external, cli)`, and
   `billing_prefix_present=true`.

2. Given `probe-oauth-mimicry-chain.sh` on edge with SDK ingress rows, when new binary
   is deployed, then docker window contains `egress_oauth_mimic` samples with
   `billing_prefix_rate ≥ 0.5`.

### Core negative

1. Given `usage_logs.user_agent=OpenAI/Python`, when operator infers upstream fingerprint
   from ingress alone, then conclusion is **wrong** — must use egress log or
   `anthropic_oauth_mimic_egress` probe field.

2. Given Kiro mirror stub (`kiro-us*`), when applying Anthropic OAuth mimicry checks,
   then verdict is **not applicable** — separate platform/protocol.

## Validation

```bash
python3 -m unittest ops.observability.test_probe_oauth_mimicry_chain -v
go test -tags=unit ./backend/internal/service/ -run 'TestTkIngressUAClass|TestTkShouldLogOAuthMimicEgress' -count=1

# Post-deploy (edge with anthropic oauth traffic):
bash ops/observability/run-probe.sh --target edge:us3 \
  --script ops/observability/probe-oauth-mimicry-chain.sh \
  --env PLATFORM=anthropic --env WINDOW_MINUTES=1440
```

Pre-change edge-us3 probe (2026-07-09): `ingress_sdk_seen_no_egress_fingerprint_logs`
(641 OpenAI/Python oauth rows; zero egress log lines — expected until deploy).
