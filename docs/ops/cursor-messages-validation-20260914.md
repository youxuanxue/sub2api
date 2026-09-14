# Cursor Messages / converter validation — 2026-09-14

## Scope and deployment state

Approved repair: Cursor AgentService → Messages is the sole native adapter;
Chat and Responses reuse existing converters. This validation does not deploy
or restart the production gateway and does not change production account settings.

Account 150 remains in groups 1 (claude) and 19 (china), with no GPT mapping.
The final read-only snapshot shows `schedulable=false`, priority 100 and concurrency
20; these operator settings were not changed by the probe.

## Latest outcome

The original supplier 57 / provider 400 continuation defect is reproduced and
fixed locally. For Opus 5, Messages passes text, initial tools and continuation
in both response modes. Chat and Responses pass text and initial tools in both
modes and buffered continuation. Their streaming continuation remains rejected
with supplier 13 / CONTENT_POLICY, so complete three-protocol acceptance is still
blocked. Nothing in this report is deployment or production billing acceptance.

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
triggered by request shape or a supplier incident. Full Claude tool continuation
and changed-code Chat/Responses live acceptance remain unverified; do not label
this account fully accepted or resume production scheduling on this evidence.

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
Messages continuation and the Chat/Responses live matrix remain unaccepted.

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
| Chat | Pass | Pass | Pass | Pass | Pass | Supplier 13 / CONTENT_POLICY |
| Responses | Pass | Pass | Pass | Pass | Pass | Supplier 13 / CONTENT_POLICY |

Thus 16 of 18 distinct matrix cells have passing evidence. Chat streaming
continuation reproduced the policy rejection twice; Responses then reproduced
it as well. Correlated requests:

- Chat: `8c92bce2-1011-4958-a813-d2a0cc07482d` and
  `6cbaf2f8-37f6-459f-872e-1921b31b4906`.
- Responses: `26f893ed-3a92-4379-8922-e7753b0fa54b`.

These failures carry Connect `invalid_argument`, supplier 13 and metadata
`CONTENT_POLICY`; they are not the earlier supplier 57 / provider 400.
The supplier supplies no detailed rejected-field explanation. A converter defect
versus an upstream policy decision is not established by these logs alone.
Three repeated failures triggered the repository pause rule; further upstream
experiments require renewed operator direction. Account scheduling stays paused.

Local validation for the empty-slot repair and diagnostic additions:
`make -C backend test` passed (all backend unit tests and lint, zero issues).
The native history regression rejects empty root content on ResumeAction.
The extended live matrix exercises streaming tool extraction and completion
markers as well as buffered JSON and billing provenance. Successful tool handoffs
use the approved estimated tier; final completions use the reported tier.

Repository preflight passed after the code changes; final documentation/sentinel
updates are checked again before the local commit. No deployment was performed.
