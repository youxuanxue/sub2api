# Cursor account 150 revalidation — 2026-09-16

Production host egress, account 150 credentials read on-host into protected
request-local files. Baseline: v1.8.231 / 151187f64b75. Account scheduling remains
paused. These are native/service-converter integration probes, not deployed
HTTP ingress, persisted billing acceptance or UI e2e. Fable 5 / 5-1 are excluded
from this investigation at the operator's request.

## Completed changes

- Removed `composer-2` from account 150 using the mapping runtime publisher's
  targeted CAS plan. Read-back dry run: zero changes, no errors. No other model
  mapping or group changed. Removed its curated row and blocked reintroduction
  through Cursor import and mapping-floor apply.
- Kept `composer-2.5` in registry / pricing / model_mapping; set public catalog
  `display=false` so it is not offered in the served menu while routing remains.
- Corrected Composer 2.5 root-history tool names to `mcp_tokenkey_<name>`.
  Captured Cursor roots use this provider name; MCP definitions/execution args
  continue to use `mcp__tokenkey__<name>`. Captured Sonnet 5 roots use the latter,
  so Claude naming is unchanged. The regression checks both namespaces and
  both call/result roots.
- Added Cursor native Messages continuation self-heal: before any client bytes,
  retry once in a fresh RunAgent conversation for explicit restore failures
  (`conversation data missing` / `missing blobs` / `can't be restored`) and
  supplier error 57 with allowlisted Connect codes (including the production
  `resource_exhausted` pairing). Policy/consent
  `ActionRequired` rejections are never retried. Bare `conversation` /
  `resume` / `blob` keywords are not enough to retry.

The naming repair and Messages retry are confirmed local mitigations, **not a
complete resolution of Composer continuation failures** or upstream variable
semantic output.

## Live evidence

| Scope | Result |
| --- | --- |
| Live authenticated catalog: Opus 5, 4.8, 4.7, 4.6, 4.5; Sonnet 5, 4.6, 4.5, 4; Haiku 4.5 | All 10 text probes returned the expected marker and native usage |
| Baseline Composer 2.5 native tool handoff/resume | Passed fresh random tool-result nonce and native usage |
| Baseline Composer 2.5 Messages buffered resume | Failed semantic nonce assertion; output was `[REDACTED]`. Sent roots and server-echoed tool roots both contain the correct nonce. Native response itself contains the replacement text |
| Composer 2.5 with corrected prompt tool name | Messages buffered and streaming text/handoff/resume passed (6 cells); Chat buffered text/handoff passed; Chat resume requested another fixture read instead of returning the nonce. Matrix stopped; remaining cells untested |
| Baseline Sonnet 5 native tool handoff/resume | Passed |
| Baseline Sonnet 5 service/converter matrix | All 18 cells passed: Messages/Chat/Responses × buffered/streaming × text/handoff/resume |
| Earlier Opus 5 service resume | Supplier 13, `CONTENT_POLICY`, `retryable=false`, `actionRequired=cyber_policy_review`; no retry or policy bypass |

The Composer Chat failure still sends the correct nonce and matching call/result
IDs. Its ID is rewritten by the shared converter from `tool_…` to `toolu_tool_…`;
this is an observed difference, not a proven explanation for the semantic failure.
No speculative ID normalization change was made.

One intermediate Composer matrix executed but its compressed trace archive
exceeded the SSM inline output limit. That run has no usable result and is not
counted as success. Subsequent collection retained remote synthetic evidence and
returned bounded summaries; the failure archive was downloaded with integrity
verification. No returned command text was executed.

## PR assessment and unresolved work

- #2171 empty-slot fix: current probes do not reproduce supplier 57/provider 400;
  Sonnet 5 passes all 18 cells. This does not establish universal continuation
  reliability for Composer or Opus.
- #2181 error handling: structured Opus policy diagnostics survive translation;
  policies remain terminal. A supplier policy rejection is not fixed by relaxing
  local classification.
- #2182 GPT exclusion remains covered; removing stale Composer 2 mapping is a
  separate account capability correction.

## Resumed official CLI / checkpoint investigation

The operator authorized this bounded round after the earlier three-failure
pause. Tests used the same production egress and account, pinned official CLI
`2026.09.02-c22c1a3`, and an isolated container permitting only MCP fixture calls
and MCP tool discovery. Returned text was never executed. Fable remains excluded.

| Controlled comparison | Result |
| --- | --- |
| Composer 2.5 official CLI, same prompt, one fixture execution | Correct fresh nonce in terminal result |
| Composer successful tool-complete checkpoint, fresh connection and conversation ID | Correct nonce and native usage |
| Same checkpoint without unknown state fields, provider options, non-tool root IDs or experimental tool content | Passed |
| Same fixture reconstructed with TokenKey's stateless history and minimal system head | Passed |
| Same reconstruction, only adding Chat's `toolu_` ID prefix to both call and result | Passed |
| Exact previously failed Chat Run payload and request context, only fresh conversation/request identity | Passed |
| Same failed history with the captured official Composer system head | Passed; original minimal head also passed, so this does not isolate a system-head defect |
| Opus 5 official CLI, one fixture execution | Correct fresh nonce in terminal result |
| Opus successful checkpoint on a fresh connection | Correct nonce and native usage |
| Composer fixed-history service matrix | Buffered Messages returned the correct nonce; streaming Messages returned `APPLE`. Stopped immediately; other cells not run |

For the last comparison, both native prompt snapshots have SHA-256
`3f3bc955a30981346411281de129158f64c51aa009ba752599d7633accbb1f25`.
Decoding the **raw upstream protobuf** confirms that the first response contains
the exact nonce and the second contains `APPLE`. The fixed-history buffered
request also matches the previous failed Chat prompt hash. This is evidence of
variable upstream output for equivalent reconstructed history, not a text
corruption introduced by SSE/Chat response conversion.

The investigation rules out a universal account/model outage, a hard requirement
for the original connection, and the ID prefix as a deterministic failure trigger.
It does **not** reveal the supplier's internal trigger or prove that generic
ResumeAction context is equally reliable as official CLI context. One successful
system-head substitution is not enough to justify copying a vendor system prompt
into production. No speculative system, history-ID or retry change was added.

The earlier Opus supplier 13 `CONTENT_POLICY` remains genuine upstream evidence.
Official CLI/checkpoint success shows it is not a blanket inability to use Opus;
no policy rejection was retried or bypassed in this round. These successes do not
establish reliable production continuation or persisted billing acceptance.

## Validation and release boundary

Local mitigations now include Composer 2 retirement, Composer 2.5 menu hide
(`display=false` with mapping retained), root-history tool-name alignment, and
Messages continuation self-heal for explicit restore / supplier-57 failures.
Root-cause isolation of the supplier's intermittent semantic output/policy
trigger remains unresolved, so account 150 stays paused. No deployment,
account unpause or policy acknowledgement is implied by these code changes.

gRPC was upgraded to 1.83.2 and required transitive versions to clear
GO-2026-6443 and GO-2026-6348; `govulncheck` should report zero reachable
vulnerabilities on the resulting tree.

Synthetic captures and compiler-overlay experiments remain in protected local
operator artifacts. Remote credentials, containers and task files and temporary
S3 transports are removed after collection. Official trace request IDs:
Composer `32e47b8d-5b34-4cef-8d8f-f3eed0cb5dac`; the subsequent replay requests
use fresh IDs and never reuse the supplier connection.
