# Claude Code (cc) fingerprint alignment — changelog

Single rolling log of cc fingerprint alignments produced by
`/tokenkey-cc-fingerprint-alignment`. **Append one row per alignment.**

Convention (see the skill §4.1/§4.2):

- **Pure UA / version bump** (no TLS ja3 change, no actionable beta-set change) →
  **no standalone doc**. The commit message + `deploy/aws/stage0/anthropic-http-mimicry-baselines.json`
  `cc_version` + the `.tls_list/*-cc-capture.bundle.json` capture bundle already are
  the record. Add **one row here** and stop.
- **A real decision** (new/removed beta token, ja3 / TLS profile change, stainless
  change, a new A/B characterization, a one-off investigation worth preserving) →
  write/update a **topic-named** `docs/spec-delta-cc-<topic>.md` (decision record,
  updated in place, referenced by code where relevant) and link it from the row.

The bimodal Haiku A/B (server-side gray release) is canonically characterized once in
[`spec-delta-cc-2.1.160.md`](spec-delta-cc-2.1.160.md) and
[youxuanxue/sub2api#429](https://github.com/youxuanxue/sub2api/issues/429); do not
re-document it per patch — note "A/B unchanged" in the row instead.

## Log

| cc version | date (UTC) | type | note |
|---|---|---|---|
| 2.1.152 | 2026-05-27 (PR #423) | **decision** | Canonical OAuth UA + beta architecture established. Records: [`spec-delta-cc-canonical-ua-beta-2.1.152.md`](spec-delta-cc-canonical-ua-beta-2.1.152.md), [`spec-delta-cc-beta-http-2.1.152.md`](spec-delta-cc-beta-http-2.1.152.md) (first bimodal Haiku observation). Runtime mechanism: [`spec-delta-cc-http-mimicry-runtime.md`](spec-delta-cc-http-mimicry-runtime.md). |
| 2.1.153 | 2026-05-28 | **decision** | Haiku beta **set** changed: added `thinking-token-count` + `structured-outputs`, dropped `claude-code` + `extended-cache-ttl`; `last-wins` variant pick. Record: [`spec-delta-cc-2.1.153.md`](spec-delta-cc-2.1.153.md). |
| 2.1.154 | 2026-05-29 | pure UA | 2.1.153 → 2.1.154. TLS/beta unchanged. |
| 2.1.156 | 2026-05-29 | pure UA | 2.1.154 → 2.1.156. TLS/beta unchanged. |
| 2.1.157 | 2026-05-30 | pure UA | 2.1.156 → 2.1.157. TLS/beta unchanged. (One-off: edge-uk1 error spike root-caused as signature-preempt logging, **not** TLS drift — now in ops memory.) |
| 2.1.158 | 2026-05-30 | pure UA | 2.1.157 → 2.1.158. Haiku A/B unchanged. |
| 2.1.159 | 2026-06-01 | pure UA | 2.1.158 → 2.1.159. TLS/beta unchanged. |
| 2.1.160 | 2026-06-02 | **decision** | Bimodal Haiku A/B canonically characterized (server-side per-request gray release); chose dominant variant A. Record: [`spec-delta-cc-2.1.160.md`](spec-delta-cc-2.1.160.md) (referenced by `gateway_service.go`, `constants.go`). |
| 2.1.161 | 2026-06-02 | pure UA | 2.1.160 → 2.1.161. Haiku A/B unchanged (per #429 / 2.1.160). |
| 2.1.162 | 2026-06-04 | pure UA | 2.1.161 → 2.1.162. Haiku A/B unchanged. |
| 2.1.163 | 2026-06-04 | pure UA | 2.1.162 → 2.1.163. TLS/beta unchanged. |
| 2.1.165 | 2026-06-05 | pure UA | 2.1.163 → 2.1.165. TLS/beta unchanged. |
| 2.1.166 | 2026-06-06 | pure UA | 2.1.165 → 2.1.166. TLS/beta unchanged. |
| 2.1.167 | 2026-06-06 | pure UA | 2.1.166 → 2.1.167. TLS/beta unchanged; Haiku A/B 8/11 vs 3/11 (per #429). Capture egress 16.147.170.3. |
| 2.1.168 | 2026-06-07 | pure UA | 2.1.167 → 2.1.168. TLS ja3 unchanged; sonnet beta unchanged; Haiku A/B bimodal 2/3 vs 1/3, baseline matches a variant (per #429). Capture egress 52.15.35.197. |
