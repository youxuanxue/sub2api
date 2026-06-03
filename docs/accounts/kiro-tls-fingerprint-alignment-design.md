# Kiro (sixth platform) TLS fingerprint alignment — design

Status: **Phase 1 shipped (capture toolchain). Phase 2 (open the gate) is the
follow-up PR — not in this one.**

## Problem

Kiro accounts egress to AWS CodeWhisperer/AmazonQ
(`codewhisperer.us-east-1.amazonaws.com`, `q.us-east-1.amazonaws.com`). TokenKey
already mimics the Kiro IDE at the **HTTP layer**: the `aws-sdk-js` style
`User-Agent` / `x-amz-user-agent` are built in `backend/internal/pkg/kiro/
constants.go` + `backend/internal/integration/kiro/headers.go`, with a per-account
`machine_id` suffix and a `KIRO_IDE_USER_AGENT_VERSION` runtime knob.

But the **TLS layer has no fingerprint masking** for Kiro. Requests go out over the
standard Go `http.Client`, so the JA3 is Go's — not a real Kiro IDE
(Electron / Node 22). Community reports (linux.do) consistently tie account bans to
client/protocol fingerprint mismatch and unstable egress IPs.

The plumbing to fix this is **already wired**: `kiroDoer` (in
`kiro_gateway_service.go`) carries a `tlsProfile` and calls
`httpUpstream.DoWithTLS(...)`, and the gateway already calls
`tlsFPProfileService.ResolveTLSProfile(account)`. The only blocker is the platform
gate in `account.go`:

```go
func (a *Account) IsTLSFingerprintEnabled() bool {
    if !a.IsAnthropicOAuthOrSetupToken() { // ← Kiro is always rejected
        return false
    }
    ...
}
```

So `ResolveTLSProfile` returns `nil` for Kiro and `DoWithTLS` falls back to plain
Go TLS. Opening this gate is clean: the canonical-HTTP pinning (cc User-Agent /
Stainless) is keyed off the **anthropic gateway path** in `gateway_service.go`
(`tlsFingerprintProfileNameForAccount` → `IsCanonicalTLSProfileName` →
`applyCanonicalHTTPObserved`); the Kiro forward path does **not** call
`GetOrCreateFingerprint`, so enabling Kiro TLS will not drag in anthropic-only HTTP
canonicalization.

## Why we cannot just reuse the cc profile

The only committed canonical TLS profile, `deploy/aws/stage0/
tk_canonical_cc_oauth.json`, was captured from a real Claude Code client
(`ja3_hash=d871...`, Node **24.3**). Kiro IDE bundles Node **22.22.0** (per its UA),
a different OpenSSL build, so its ClientHello — and therefore its JA3 — is very
likely different. Reusing cc's profile, or hand-crafting a JA3 from the known UA,
would be a **confident-but-wrong fingerprint**: potentially *easier* to flag than
Go's default. So the canonical Kiro profile must come from a **real capture with
provenance**, exactly like the cc one.

## Why the capture method differs from cc

cc capture works by redirecting Claude Code to a self-hosted TLS collector via
`ANTHROPIC_BASE_URL` + a cc0 proxy MITM (`ops/anthropic/capture-cc-fingerprint.sh`).
The **real Kiro IDE hard-codes the CodeWhisperer endpoint and cannot be
redirected.** Therefore:

- **TLS (primary, deterministic):** passive `tcpdump` of the real Kiro IDE
  handshake to the CodeWhisperer IPs → `tshark` extracts the ClientHello (sent in
  the clear, no MITM) → JA3 computed with GREASE stripped.
- **HTTP UA (secondary, best-effort):** only if the Kiro IDE honors `HTTP_PROXY`
  + a trusted MITM CA, `mitm_kiro_http_headers.py` records the on-wire UA to
  confirm it equals the constant-derived UA. If the IDE ignores the proxy, JA3 from
  pcap is the load-bearing signal and the UA is confirmed manually.

## Phase 1 deliverables (this PR)

Under `ops/kiro/` (parallel to cc's `ops/anthropic/`):

| File | Role |
|---|---|
| `capture-kiro-fingerprint.sh` | Passive pcap orchestrator: resolve CodeWhisperer IPs → `tcpdump` handshake → `tshark` ClientHello (SNI-filtered to `amazonaws`) → call the engine → diff. Also `diff` / `check` / `check-tls` / `show-baseline` / `emit-profile` pass-throughs. |
| `capture_kiro_fingerprint.py` | Deterministic engine (stdlib): rebuild expected UA from `kiro/constants.go`, parse the tshark TSV, compute `ja3_raw`/`ja3_hash` (GREASE-stripped, md5), assemble an upstream-shaped TLS profile, diff vs the committed profile, exit-code gating. Pure functions unit-tested. |
| `mitm_kiro_http_headers.py` | Optional mitmproxy addon for best-effort UA confirmation. |
| `test_capture_kiro_fingerprint.py` | Unit tests for JA3/GREASE, UA rebuild, TSV parse, profile build, diff gating. |

Phase 1 does **not** touch any Go code, does **not** open the gate, and does
**not** fabricate a profile. `deploy/aws/stage0/tk_canonical_kiro_ide.json` is
produced only by running `emit-profile` against a **real** capture (committed as a
follow-up commit once a real Kiro IDE ClientHello is captured).

## Capture runbook

```bash
# On a host running a real, logged-in Kiro IDE (tcpdump needs sudo).
# Direct egress:
bash ops/kiro/capture-kiro-fingerprint.sh capture --iface en0 --seconds 60
# Proxied egress (Electron follows the macOS system proxy, e.g. Clash on 7890 —
# the cleartext ClientHello is on loopback to the proxy port before it is
# re-encrypted onward, so capture lo0 + that port):
bash ops/kiro/capture-kiro-fingerprint.sh capture --proxy-port 7890 --seconds 75
#   → trigger ONE Kiro IDE request when prompted (e.g. send a Kiro chat message)
#   → prints ja3_hash + a diff (first capture = "missing_tokenkey", non-actionable)

# Commit the canonical profile from the real capture:
python3 ops/kiro/capture_kiro_fingerprint.py emit-profile \
    --bundle .kiro_tls/<stamp>-kiro-capture.bundle.json
#   → writes deploy/aws/stage0/tk_canonical_kiro_ide.json

# Re-run later to detect drift (e.g. after a Kiro IDE update):
bash ops/kiro/capture-kiro-fingerprint.sh check --bundle .kiro_tls/<stamp>...bundle.json
```

### First real capture (provenance)

Captured 2026-06-03 from a real Kiro IDE on macOS (Node 22.22.0), proxied egress
via the system proxy on `127.0.0.1:7890`, SNI `q.us-east-1.amazonaws.com` (the
`EndpointKiroIDE` primary), 6 byte-identical ClientHellos in the window:

- `ja3_hash = 51bddd625044f75a235ba857ac8b0145`, no GREASE.

This **differs** from the cc canonical (`d871d02cecbde59abbf8f4806134addf`, Node
24.3) — Kiro carries one extra cipher (`0xc027`), a different cipher order, and a
different extension set (no `16`/ALPN, no `5`/status_request, no `18`/SCT) — which
confirms the profiles must not be shared.

## Phase 2 (follow-up PR — open the gate)

Once `tk_canonical_kiro_ide.json` exists from a real capture:

1. **Gate:** in `backend/internal/service/account.go`, add a Kiro-aware predicate
   (e.g. `IsKiroOAuth()`) and let `IsTLSFingerprintEnabled()` accept Kiro accounts.
   Keep the anthropic-only canonical-HTTP path untouched (verify no Kiro forward
   path reaches `applyCanonicalHTTPObserved`).
2. **Seed:** migration `tk_0xx_seed_kiro_ide_tls_template.sql` (mirror
   `tk_011_seed_claude_code_template`) upserts the profile row into
   `tls_fingerprint_profiles`.
3. **Bind:** set each Kiro account's `extra.enable_tls_fingerprint=true` +
   `extra.tls_fingerprint_profile_id=<id>`; when done via raw SQL, also enqueue a
   `scheduler_outbox` row so running replicas reload the account snapshot.
4. **Lock:** add `identity_canonical_consistency_kiro_test.go` asserting the
   constant-derived UA stays in lockstep with `kiro.BuildUserAgent`.
5. **Verify streaming:** the EventStream reader (`integration/kiro/client.go`
   `parseEventStream`, `io.ReadFull`) is TLS-transparent, so utls does not affect
   streaming — confirm with a live smoke after enabling.

## Out of scope

- `kiro-us1-real` `concurrency` 0→5 (community anti-ban ceiling) — operational,
  done via admin UI or `UPDATE accounts ... WHERE platform='kiro' AND
  concurrency<=0` + `scheduler_outbox`, not in this PR.
- Reusing the anthropic **tier** mechanism for Kiro — rejected: tier semantics
  (5h window / sessions / RPM zones) are anthropic-subscription-specific and do not
  map to AWS CodeWhisperer.
