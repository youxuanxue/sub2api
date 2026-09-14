# Cursor Messages / converter validation — 2026-09-14

## Scope and deployment state

Approved repair: Cursor AgentService → Messages is the sole native adapter;
Chat and Responses reuse existing converters. This validation does not deploy
or restart the production gateway and does not change production account settings.

Account 150 remains in groups 1 (claude) and 19 (china), with no GPT mapping.
The final read-only snapshot shows `schedulable=false`, priority 100 and concurrency
20; these operator settings were not changed by the probe.

## Latest outcome

The supplier 57 / provider 400 continuation defect is reproduced and fixed
locally. All 18 Opus 5 protocol/mode/scenario cells now have passing evidence,
including Chat and Responses streaming continuation. Equivalent native history
can also receive supplier 13 / CONTENT_POLICY: the retained ErrorDetails explicitly
says Anthropic Usage Policy, `isRetryable=false`, and
`analyticsMetadata.actionRequired=cyber_policy_review`. This is a supplier policy
refusal, not evidence of a Chat/Responses-specific converter defect. The supplier's
internal trigger and whether any account review is required remain unproven.

The repair maps native structured facts to the existing shared usage/cyber policy
owner. Policy refusals terminate once, without retry, failover, account cooldown
or successful settlement. Existing handler session isolation and zero-token error
usage remain the owners. No new live policy retries were made for this repair.
Nothing in this report is deployment or production billing acceptance.

## Confirmed code repairs

- Do not replay empty reserved system/user prompt slots in `ResumeAction`.
  Only `UserMessageAction` populates those slots; reconstructed continuation
  supplies a nonempty system head and the actual history.
- Preserve safe Connect codes and bounded/redacted ErrorDetails in correlated
  operator logs; HTTP status and translated Connect status remain distinguishable.
- Inspect native completion before buffered success. Native and Edge-relayed
  failures cannot fabricate successful JSON, stop/completed events or settlement.
- Keep response-mode ownership: committed JSON never receives SSE; a native
  Messages terminal error does not receive a second fallback event.
- Preserve Cursor tool-result outer IDs and consistently normalize foreign tool
  IDs in both prompt and protobuf history. Detect normalized-ID collisions.
- Continue reading terminal usage when a streaming client disconnects.

## Earlier upstream evidence (before the empty-slot repair)

These are API integration probes of the modified adapter and converters using
account 150's current catalog/credentials. They are not UI e2e, production routing
or persisted production billing acceptance. Each run stops at its first failure.

| Egress / stage | Observation |
| --- | --- |
| Local / Opus 5 text | Connect `resource_exhausted`, supplier error 29, `Model not available`: provider unsupported in this region. This does not explain the earlier production errors. |
| Production / Opus 5 text | Passed; native reported usage present. |
| Production / Opus 5 initial tool call | Passed; declared fixture tool and arguments matched; successful handoff used the approved estimated tier. |
| Production / tool continuation | Connect `resource_exhausted`, supplier error 57, `Provider Error`: Cursor reports trouble connecting to the model provider. No completed answer or settlement was accepted. |
| Production / continuation after history-ID repair | Same supplier error 57. The history-ID repair is not proven to resolve this rejection. |

Correlated native requests:

- Local region rejection: `f8758ca1-44e2-446c-9bb6-5e0138de3c19`.
- Production continuation: `308cae5d-7214-46ae-aa95-11abbaf8a299`.
- Production continuation after history repair: `4961432a-bfa3-40dd-8f08-93ef83dacb35`.

The original production `invalid_argument`/Fable failures did not retain native
ErrorDetails. Their exact rejected fields cannot be reconstructed from those logs.
The new generic Provider Error does not prove whether the supplier fault was
triggered by request shape or a supplier incident. Those early samples alone did not validate continuation; the later controlled
evidence below supersedes that uncertainty. Production scheduling remains paused.

## Reproducible local checks

- Native tests: diagnostics/redaction, malformed frames, terminal usage, tool
  pairing, root IDs, foreign IDs, collision rejection and cancellation.
- Service tests: Messages/Chat/Responses × buffered/streaming × completion,
  handoff, stateless resumed history and failure; Edge relay failure/settlement;
  client-disconnect terminal usage.
- Handler tests: JSON preservation and single SSE error ownership.
- Full backend tests and lint: `make -C backend test` — passed.
- Repository gate: `PREFLIGHT_BASE=origin/main bash scripts/preflight.sh` — passed.

Protected temporary credentials, remote probe processes and temporary S3
transport objects are cleaned after validation. No credentials are committed.


## Official CLI differential evidence (initial round, 2026-09-14)

Same account 150, model `claude-opus-5`, production host egress, and pinned
CLI `2026.09.02-c22c1a3`. The CLI ran in an isolated, temporary container with
only a synthetic MCP fixture workspace. No account settings were changed.

- Official CLI passed: one fixture execution generated a fresh UUID nonce;
  the terminal `result` contained that nonce. Exit status alone was not the
  acceptance criterion. An earlier ask-mode run refused the fixture execution
  and is not counted as fixture success.
- Official capture used one Run stream; tool results returned in
  `ExecClientMessage`. TokenKey cancels for external handoff, then sends a new
  Run with reconstructed root history and `ResumeAction`.
- Native TokenKey baseline failed at continuation, request
  `1a1af826-e542-4ef4-a67a-d45eacd57fc0`.
- Newly decoded `CustomErrorDetails.additional_info` reveals
  `providerStatusCode=400`, with trailer metadata `PROVIDER_ERROR`.
  Cursor wraps this as supplier 57 / Connect `resource_exhausted`; the existing
  adapter maps that to HTTP 429. This is not evidence of provider throttling.
- Single-variable tool-result `experimental_content` addition still failed:
  `5dcc5f9c-f785-454d-8972-b5980c0ece52`, same supplier 57 / provider 400.
- Single-variable assistant `providerOptions.cursor.anthropicNativeContent`
  addition still failed: `a7172a24-2495-4eeb-a55a-84defe175d67`, same rejection.
  These variants used a test compiler overlay, not production source changes.
- One preliminary experiment failed locally on an unknown history blob because
  its wire mutation did not update the local blob store. The corrected overlay
  updates both root references and the store; only those results count above.

The supplier returned no field-level validation message. Official success narrows
investigation toward reconstructed-history / new-run compatibility, but does not
prove cancellation, ResumeAction, or any single omitted field is the root cause.
Three completed continuation attempts reproduced the same failure, so further
live experiments paused under the repository's three-failure rule.

Recommended next bounded investigation: replay a captured successful official
checkpoint and blob set in a fresh Run, then reduce that state one variable at a
time. This distinguishes a missing state requirement from inability to resume
across connections before proposing any production session-storage architecture.
At that checkpoint, Messages continuation and the converter matrix were unaccepted;
the subsequent sections record the resumed investigation.

Current diagnostic additions retain redacted bounded additional-info, trailer
metadata and an opaque-detail inventory. The opt-in native probe is
`TestAgentLiveToolComparison`; it requires a protected account file and does not
change scheduling. Native package tests were rerun for these additions; previous
full-backend/preflight passes above apply to the committed baseline, not a new
release of these uncommitted diagnostics.


## Checkpoint replay and confirmed trigger (resumed investigation)

All comparisons use account 150 / `claude-opus-5` / production egress.
The captured official checkpoint is immediately after the fixture result and
before the final assistant answer; its final answer is not replayed as input.

| Controlled variation | Result |
| --- | --- |
| Official checkpoint + blobs, new connection and conversation ID | Passed, expected nonce and usage present |
| Same, removing unknown conversation-state fields | Passed |
| Same, additionally removing root provider metadata, non-tool outer IDs and experimental tool content | Passed |
| Official checkpoint with first two root messages replaced by TokenKey's empty system/user slots | Reproduced supplier 57 / provider 400 (`8ef68d0e-825c-4efd-a252-cf0aecce76e9`) |
| TokenKey stateless continuation with empty slots removed and a nonempty system head | Passed initial handoff and resumed nonce answer with native usage |

The first official replay attempt retained TokenKey's MCP-only header, causing
`Required tool GET_MCP_TOOLS not found in allTools`; that replay setup error was
corrected in the isolated probe only. Production's MCP-only header is unchanged.
No session store, persistent connection, official runtime dependency or local
tool execution was added to the production adapter.

One native fixed-code attempt also returned supplier 13 / CONTENT_POLICY
(`f9af80f3-3afd-4150-930d-428dd7401870`); the subsequent identical test passed.
This is retained as a separate failure, not relabeled as the resolved supplier 57.

## Fixed-code service/converter matrix

The service probe uses actual registered plans, Messages adapter and converters
against the real upstream from the production host. HTTP ingress is an in-process
recorder: these are API integration tests, not UI e2e, deployed-gateway routing,
or persisted production billing verification. Each run stops at the first error.
All input is synthetic fixture data; final answers must contain a freshly generated
nonce. Streaming output must carry the protocol's successful terminal marker.

| Protocol | Buffered text | Buffered tool call | Buffered continuation | Streaming text | Streaming tool call | Streaming continuation |
| --- | --- | --- | --- | --- | --- | --- |
| Messages | Pass | Pass | Pass | Pass | Pass | Pass |
| Chat | Pass | Pass | Pass | Pass | Pass | Pass; also policy refusals |
| Responses | Pass | Pass | Pass | Pass | Pass | Pass; also policy refusals |

The initial matrix had 16 of 18 passing cells. Before the later fixed-history
comparison, Chat and Responses streaming continuation returned policy refusals.
Correlated requests:

- Chat: `8c92bce2-1011-4958-a813-d2a0cc07482d` and
  `6cbaf2f8-37f6-459f-872e-1921b31b4906`.
- Responses: `26f893ed-3a92-4379-8922-e7753b0fa54b`.

These failures carry Connect `invalid_argument`, supplier 13 and metadata
`CONTENT_POLICY`; they are not the earlier supplier 57 / provider 400.
These initial logs did not decode the analytics action. Three repeated failures
triggered the pause rule; the user subsequently authorized the bounded comparison
below. Account scheduling stays paused.

Local validation for the empty-slot repair and diagnostic additions:
`make -C backend test` passed (all backend unit tests and lint, zero issues).
The native history regression rejects empty root content on ResumeAction.
The extended live matrix exercises streaming tool extraction and completion
markers as well as buffered JSON and billing provenance. Successful tool handoffs
use the approved estimated tier; final completions use the reported tier.

Repository preflight passed after the code changes; final documentation/sentinel
updates are checked again before the local commit. No deployment was performed.

## Fixed-history policy comparison and SSOT repair

One synthetic handoff/history/nonce was held fixed across all three protocols
and both modes. Every resumed normalized prompt had SHA-256
`230b99f5ae4c64d882348b50ea48d0425589e21f4023ce31c48f0f6acadeaf45`.
Full native comparisons additionally covered root history, steps, model, tool
schemas and request context. Fresh UUIDs and protobuf map serialization order
differed; semantic content matched. This is not a byte-identical wire claim.

- Messages → Chat → Responses order: Messages both passed, Chat both passed,
  Responses buffered passed, Responses streaming refused
  (`07e4f1b4-bc26-4a87-8110-8433859d6d2e`).
- Responses → Chat → Messages order, same fixture: Responses both passed,
  Chat streaming refused (`cf20c700-1955-4e75-9ab7-88120602ea91`); stopped there.

Both remaining cells therefore have successful native usage and nonce evidence.
The differing outcomes for equivalent history do not justify policy retries or
wire-order manipulation. No further live refusal probes followed the user's
instruction to use existing usage/cyber policy and no-retry SSOT.

Pinned CLI ErrorDetails field 2 / CustomErrorDetails field 10 /
ErrorAnalyticsMetadata field 1 provides the structured `cyber_policy_review`
action. Only title/detail and this action feed the bridge; arbitrary echoed
additional-info, opaque details and raw prompts cannot classify a session.
Supplier metadata remains bounded/redacted operator evidence, with fixed safe
public error text.

`cursorPublicPolicyError` translates these native facts into the existing
`markOpenAISafetyPolicyEvent` vocabulary. Native HTTP and SSE consumers mark the
same request context before returning the existing forwarded-policy sentinel.
Chat emits one error chunk, Responses one response.failed, and Messages retains
one native error. Canonical error codes survive the Messages relay wire. No new
session store, policy detector, account punishment, retry loop or billing path
was introduced. Existing handler policy tests cover isolation and zero-token
error accounting; the new native matrix covers early/late policy rejection in
all three protocols and both modes, one native attempt and no successful result.

Local policy validation: `make -C backend test` passed (backend unit suite and
lint, zero issues). The final Messages JSON/SSE relay additions also passed
`go -C backend test -tags=unit ./internal/service -run
'Test(Cursor|NativeMessagesPolicy)' -count=1`. These are offline tests; no further
policy-rejected requests were sent upstream. Remote account snapshots, probe
binary and signed URL, the private S3 transport object, and local probe artifacts
were removed; protected synthetic traces remain for audit.
