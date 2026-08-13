# Kiro CLI fingerprint alignment

## Decision

TokenKey has one Kiro client identity: the real Kiro CLI. Kiro platform behavior—OAuth
refresh, translation, EventStream decoding, models, scheduling, billing, admin UI, and
protocol fallbacks—remains unchanged. Only the retired alternate client identity and its
duplicate operational surfaces are removed.

```text
real kiro-cli
  → sanitized TLS + HTTP + protocol + auth evidence
  → one HTTP owner
  → tk_canonical_kiro_cli
  → one runtime DB row
  → one release watcher / observer / remediation path
```

Release metadata and binary strings are discovery evidence only. They cannot prove HTTP
identity or ClientHello semantics. Missing evidence stays `NOT_OBSERVED`.

## Evidence contract

The capture engine runs the installed, logged-in `kiro-cli` through a local MITM collector
for a deterministic minimal operation. A valid bundle contains four independent lanes:

| Lane | Required observation |
| --- | --- |
| TLS | At least three real ClientHellos with stable semantic fields |
| HTTP | Request method/host/path plus allowed identity headers |
| Protocol | Target, content type, opt-out value, and request-body key projection |
| Auth | Mechanically classified cohort from sanitized `whoami` + cache metadata |

The collector discards authorization, tokens, client secrets, profile ARNs, user content,
complete bodies, response bodies, and key-share bytes before writing evidence. The unified
exit contract is `0=complete/aligned`, `1=drift`, `2=invalid/error`, and
`3=incomplete/NOT_OBSERVED`.

The captured CLI 2.18.0 identity is owned by:

- `backend/internal/pkg/kiro/constants.go`: exact HTTP identity constants.
- `backend/internal/integration/kiro/headers.go`: single consumer used by streaming and
  management/runtime requests.
- `deploy/aws/stage0/tk_canonical_kiro_cli.json`: TLS capture provenance and runtime
  projection.

## TLS semantics

The real CLI uses rustls. Cipher order, groups, signatures, supported versions, key share,
PSK modes, point formats, and extension membership are stable. Extension order is permuted
for each ClientHello; therefore:

- the canonical profile stores `shuffle_extensions=true`;
- individual JA3 hashes and extension orders remain observational samples;
- semantic comparison sorts extensions only when shuffle is enabled;
- capture and replay require at least three samples and at least two distinct orders;
- all other ordered arrays remain order-sensitive.

`TLSFingerprintProfile.ToTLSProfile()` carries this field to the uTLS dialer, which permutes
the configured extension list per handshake. The replay gate captures TokenKey-generated
ClientHellos with distinct provenance and compares their semantic projection to the real CLI
profile. A replay does not become real-client evidence merely because it matches.

## Runtime and database cutover

`CanonicalKiroTLSProfileName` resolves `tk_canonical_kiro_cli` for Kiro accounts. Migration
`tk_079_kiro_cli_tls_profile.sql` atomically:

1. upserts the complete CLI runtime projection;
2. rebinds only accounts explicitly referencing the old canonical row;
3. emits scheduler refresh events for those accounts;
4. proves no old-ID references remain;
5. removes the retired row.

Operator-created profiles are untouched. The migration is idempotent. Published migration
`tk_014_seed_kiro_ide_tls_template.sql` is immutable history and is not a current owner;
changing history would make already-deployed databases non-reproducible.

The canonical JSON is capture provenance and current projection. The DB row is runtime
configuration. `ops/kiro/check_kiro_tls_profile_parity.py` compares the two, while
`ops/observability/probe-kiro-tls-profile-parity.sh` performs a read-only production snapshot.
This proves `production_configured` parity only, not a deployed live ClientHello.

## Runbook

```bash
# Real-client capture; requires logged-in CLI, mitmdump, and its local CA.
bash ops/kiro/capture-kiro-fingerprint.sh capture --samples 3

# Complete evidence/diff gate.
bash ops/kiro/capture-kiro-fingerprint.sh check \
  --bundle .kiro_tls/<stamp>-kiro-cli.bundle.json

# Refresh committed projection only from valid real evidence.
bash ops/kiro/capture-kiro-fingerprint.sh emit-profile \
  --bundle .kiro_tls/<stamp>-kiro-cli.bundle.json

# Validate captured TokenKey/uTLS replay semantics.
bash ops/kiro/capture-kiro-fingerprint.sh check-replay \
  --tls-jsonl /tmp/kiro-tokenkey-replay-tls.jsonl

# Validate local canonical projection; production snapshot remains read-only automation.
python3 ops/kiro/check_kiro_tls_profile_parity.py
```

Raw evidence lives under ignored local paths and must never be committed. Any runtime-field
change requires a new forward migration; never rewrite an already-published migration.

## Invariants

- One Kiro registry identity: `kiro-cli`, evidence mode `wire_tls_http`.
- One compile release owner and one pair of HTTP identity constants.
- One canonical profile name and one production parity observer.
- No alternate identity mode, fallback flag, version environment override, per-account
  identity suffix, compatibility alias, or retired runtime row.
- No credentials or private request content in evidence, tests, reports, or git.
