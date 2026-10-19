---
title: OAuth search history compatibility with unchanged tool permissions
status: approved
approved_by: "user (conversation approval: 同意。继续。; namespace text prototype: 很好，继续修复，开 pr)"
approved_at: 2026-10-19
created: 2026-10-19
scope: "OpenAI OAuth Responses hosted-search history normalization and bounded failure diagnostics"
---

# Decision

When OAuth Responses replays `web_search_call` without a search declaration,
repair the outgoing request in the existing history normalizer. Customers
continue sending their existing requests. Preserve historical information, tool
definitions, and the original set of executable tools. Ordinary tools use a
restricted search declaration; supported namespaces use textual search history.

The production experiment on 2026-10-19 held the account/model fixed: ordinary
function requests completed; adding hosted-search history without a declaration
produced `response protection is unavailable`; adding cached-only `web_search`
and restricting `allowed_tools` to the original function completed. This proves
the compatibility gap, not every field of the historical customer request,
whose stored request body was truncated.

# Contract

- No-tools or explicit `none`: retain the existing no-execution repair.
- Supported non-namespace tools with default/`auto`/`required`: declare cached-only search for
  historical validation and restrict selection to the original executable tools,
  preserving auto versus required selection.
- Explicit tool selections and existing allowed-tool restrictions must not gain
  access to the newly declared search tool. Invalid or unsupported restrictions
  remain unchanged for upstream validation.
- Namespace declarations remain namespaces. With default/`auto`/`required`
  selection, completed search records with an action object become assistant
  `output_text` messages containing the complete original search JSON, explicitly
  labeled as historical data. Keep all other history items (including reasoning
  and citations), item order, declarations and tool choice unchanged; never inject
  a search declaration on this branch. Only function/custom namespace members and
  otherwise supported companion tools qualify.
- Namespace explicit/allowed selectors, unknown or malformed tool shapes,
  unfinished/malformed search records and non-null `previous_response_id` stay
  unchanged. No flattening, selector guessing or deletion of reasoning/history.
  Explicit `none` retains the existing declaration-only no-execution repair.
- Standard Responses uses top-level tools. Lite uses `additional_tools` before
  the compaction trigger for declaration repairs; textual repair keeps carriers
  in place and preserves all non-search raw history values.
- API-key and legacy `/responses/compact` exclusions, account failover decisions,
  provider error attribution and client status semantics remain unchanged.
- On protection failures, emit bounded structural evidence and reason codes only;
  never log request content, tool names/descriptions, ciphertext or credentials.

# Owners and acceptance

`backend/internal/service/openai_oauth_web_search_history_tk.go` remains the
normalization owner; existing Forward and compatibility paths consume it.
Acceptance covers ordinary and Lite requests, function/custom/namespace tools,
explicit/allowed/required choices, malformed declarations, idempotence and
history precision. Forward tests inspect the actual outgoing body. Live synthetic
validation uses reserved probe resources and verifies SSE terminal success and
account-attributed usage. Unsupported upstream shapes must not be broadened.

The user approved implementation after reviewing the production experiment and
permission-preserving design. Merge/deployment is a separate release action.

# Evidence and limitations

On 2026-10-19, reserved gateway probes used account 259 and `gpt-5.6-sol`.
The ordinary function repair completed with `allowed_tools` (request
`295d95ff-8440-49a4-962a-75dacef4fef3`). Lite required-function requests were
attributed to the same account with both top-level declarations
(`5e69936b-d422-4a2f-8d7f-b4a2291b9587`) and `additional_tools`
(`abfdfc15-f471-4b62-87ce-40ab8f0fe0cf`). Both returned `response.completed` with the original function call and zero
search output calls. A standard custom-tool required selector also completed
(`ba0fe130-f4ab-4625-a641-66ffb02d9e10`). Raw SSE terminals are checked
separately from HTTP status; HTTP 200 alone is not success.

Namespace declarations with cached-only search and auto selection completed
and returned the original `namespace=diagnostic`, `name=lookup` function call
(`e17bd0bd-a53d-48ba-9ce1-94d9d1235737`). This control is diagnostic evidence,
not an acceptable production repair: it leaves search executable. Upstream
rejected namespace-type selectors, member selectors with a namespace field,
and bare/qualified member-name selectors. Qualified member selection also
failed under Lite (`9974853f-3782-4857-9f75-8e95c0066a03`). No general permission-preserving
namespace **selector** repair has been validated. Instead, the user approved the
textual-history prototype after the following fixed-account synthetic checks:

| Scenario | Request ID | Outcome |
|---|---|---|
| Native history, standard | `ac6c8232-9fdc-4386-9560-53f23784b971` | Same protection failure, account 259 / semantic 502 |
| Native history, compaction | `084e5c52-36c6-42a7-8097-fbd29b29eab7` | Same protection failure, account 259 / semantic 502 |
| Text history, namespace function/custom | `a02d3b71-21af-4673-a63f-fd84c288fef1`, `3b78f00b-5681-46d3-acf6-294641c22e9b` | Completed with original namespace calls |
| Lite text history, namespace function/custom | `f9acdf9f-572c-4f9c-9aba-0f7bc0bf8939`, `d5060dac-2f0d-4390-9614-8f94616fbc66` | Completed with original namespace calls |
| Standard/Lite text compaction | `28d451df-5427-49cd-a657-1acae9993a69`, `37686178-e0d6-42e7-8c4b-ccf5b750fcf4` | Completed with actual encrypted compaction items |
| Standard/Lite recall from compaction only | `ffcbac26-9c55-4329-a66d-09f5db06bd5c`, `4f7140fc-6336-4374-8315-bfdc9bd33399` | Correct namespace call and all tested historical facts |

The common seed was real hosted-search output with encrypted reasoning and URL
citations (`fe0c3a83-6b7b-4e68-ab9d-09296ffb133f`). Calls correctly recalled the
synthetic project code, count, URL and exact first search query. Recall requests
contained only the compaction item, latest question and original tool definitions.
All repaired probes were attributed to account 259, kept `auto` and the original
two namespaces, and emitted no new search calls. These live checks cover `auto`;
default/null/required preservation is covered by local structural tests. The
prototype was tested with one short history/model/account, not arbitrary long
sessions or every search action. Preserving visible JSON does not prove complete
equivalence of upstream hidden tool state. Reserved probe resources were disabled
and bindings removed afterward.

The bounded failure diagnostic includes namespace count, distinguishes
`repairable_history_text`, and examines the full inbound body beyond the QA prefix.

The historical user 16 request's full tool choice and search history were not
retained in QA. This patch addresses the reproduced compatibility mechanism;
it does not establish that namespace customer requests are fixed. Production
release and customer-session verification remain separate acceptance steps.
