# Kiro Tool History Fidelity

## Background

`sanitizeKiroHistory` treated all but the active tool pair as invalid, removed
assistant calls and narrated results into ordinary user text. This erased the
structured execution record and duplicated current results as prose. #2068
corrected `is_error` mapping but left this history policy in place.

Real Kiro CLI 2.21.1 v1/v2 experiments on 2026-09-09 retained earlier calls and
results across a failed fixture read followed by successful reads. These used
controlled EventStream responses and real local tool execution; they were not
successful upstream model requests. The public predecessor corroborates pairing:
`aws/amazon-q-developer-cli@15cc8f3cd18c4272925ce1c7053268eedff1ea0a`,
`crates/agent/src/agent/mod.rs` (`enforce_conversation_invariants`).

## Delta

- Preserve valid calls, inputs, result IDs, content and status across history.
  Keep tool specifications on the current message only. Normalize call names
  using the corresponding translator's existing tool-spec naming rules.
- Use one `normalizeKiroToolHistory` owner for both request translators and
  private completion continuation. Join split user result batches without
  dropping distinct results with identical content.
- Narrate only orphan results. Represent missing client results as explicit
  errors stating execution is not confirmed, without claiming user cancellation.
- Retain `status=error` and prefix failed tool content with an idempotent failure
  marker. Preserve the original content after the marker; successful content
  remains unchanged. This is execution-state data, not a completion prompt.
- Trim history at pair boundaries. Current calls/results take priority when the
  preferred recent history cannot fit. No change to terminal stream validation,
  client-visible stop reasons or the private completion acceptance policy.

## Scenarios

- CLI-style multi-turn failed/successful results and hidden continuation:
  `TestClaudeToKiro_CLIToolHistory`, `TestClaudeToKiro_PreservesToolResultFailure`.
- Partial results and orphans with ordinary user instructions:
  `TestClaudeToKiro_RepairsOnlyUnpairedResults`.
- Parallel/split batches, tool names and normalization idempotence:
  `TestOpenAIToKiro_ParallelToolHistory`,
  `TestClaudeToKiro_SplitResultsAndToolNames`.
- Large historical results and active pair retention:
  `TestClaudeToKiro_ToolHistoryTruncation`.
- Actual serialized gateway requests in both streaming modes:
  `TestKiroGatewayService_ContinuationPreservesToolHistory`.

## Validation

Local package and gateway regression tests run with:

```sh
cd backend
go test -tags=unit ./internal/integration/kiro ./internal/service -run 'Kiro|CompletionSignal'
```

Live synthetic upstream probes used us4 account 17, `claude-opus-5` and TokenKey's
`/generateAssistantResponse` path on 2026-09-09. The body came from the real Go
translator; no production configuration was changed. Complete CRC-checked
EventStream responses returned HTTP 200 and `END_TURN` with all historical pairs
present in the request. With neutral result content and only `status=error`, the
model answered that all reads succeeded. Adding the failure marker inside that
same failed tool result produced the expected answer `fixture-0`.

SSM evidence: baseline `4060dafc-9e00-4ce5-a086-e968aa342ce4`; marked failure
`04d6f308-b382-4df1-ae75-c79a5d9cbd22`. A follow-up without current tool specs
also returned HTTP 200 and `fixture-0` (`6a892f71-4742-45e6-ba71-5b2298ab4fd9`),
so retained historical calls did not require adding dummy callable tools in
this probe. This supports request compatibility and
the explicit failure-content adaptation. It does not establish an upstream
implementation cause or a measured reduction in user 16's hallucination rate.
