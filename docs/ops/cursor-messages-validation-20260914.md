# Cursor Messages / converter validation — 2026-09-14

## Scope and deployment state

Approved repair: Cursor AgentService → Messages is the sole native adapter;
Chat and Responses reuse existing converters. This validation does not deploy
or restart the production gateway and does not change production account settings.

Account 150 remains in groups 1 (claude) and 19 (china), with no GPT mapping.
The final read-only snapshot shows `schedulable=false`, priority 100 and concurrency
20; these operator settings were not changed by the probe.

## Confirmed code repairs

- Preserve safe Connect codes and bounded/redacted ErrorDetails in correlated
  operator logs; HTTP status and translated Connect status remain distinguishable.
- Inspect native completion before buffered success. Native and Edge-relayed
  failures cannot fabricate successful JSON, stop/completed events or settlement.
- Keep response-mode ownership: committed JSON never receives SSE; a native
  Messages terminal error does not receive a second fallback event.
- Preserve Cursor tool-result outer IDs and consistently normalize foreign tool
  IDs in both prompt and protobuf history. Detect normalized-ID collisions.
- Continue reading terminal usage when a streaming client disconnects.

## Real upstream evidence

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
