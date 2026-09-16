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
- Corrected Composer 2.5 root-history tool names to `mcp_tokenkey_<name>`.
  Captured Cursor roots use this provider name; MCP definitions/execution args
  continue to use `mcp__tokenkey__<name>`. Captured Sonnet 5 roots use the latter,
  so Claude naming is unchanged. The regression checks both namespaces and
  both call/result roots.

The naming repair is a confirmed wire-format alignment, **not a complete
resolution of Composer continuation failures**. It has not been deployed.

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

Composer semantic failures remain unresolved after three unsuccessful service
investigation runs across the retained evidence, including the name-alignment
candidate. Further live Composer experiments are paused under AGENTS.md's
three-failure rule. Next investigation needs an explicitly authorized bounded
comparison against official CLI/checkpoint behavior; it must not retry or bypass
policy refusals. The supplier's internal policy trigger for Opus remains unknown.

Local validation: focused native/service Cursor and import regressions pass.
Final repository preflight status is recorded in the accompanying PR. No merge,
release, deployment, account unpause or policy acknowledgement is included.
