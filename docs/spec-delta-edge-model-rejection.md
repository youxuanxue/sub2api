# Edge Model Rejection During Candidate Retry

## Background

Prod private replay on 2026-09-10 found Sonnet Messages requests stuck on an
Antigravity edge relay. Cross-pinning the same request to the same account proved
both 1.8.215 and 1.8.216 fail on account 62 and succeed on account 69. Independent
soft-session bindings made the first comparison appear version-dependent.

The us4 Antigravity key belongs to a group with Messages dispatch disabled.
Its edge rejects the request before account selection, while the prod mirror
still advertises Sonnet. This is path unavailability, not proof that the model
is unavailable through all authorized origins.

## Delta

The existing candidate failover loop accepts the known edge's exact HTTP 400
`invalid_request_error` / `Unsupported model: <current model>` envelope before
response commitment. Admission must already have selected the same account and
model. Native Messages and OpenAI-compatible native-Messages forwarding share
one classifier. The failed account is excluded only for the current request;
there is no same-account retry, credential penalty, global negative cache,
account configuration change, or expansion of authorization.

If all authorized candidates fail, the existing bounded loop returns an upstream
502 using the explicit client-facing override. Unrelated client 400s remain
terminal. The canonical request is preserved across retries.

## Scenarios

- A sticky Antigravity relay rejects Sonnet; an authorized Kiro relay succeeds.
- Universal billing follows the successful group's origin; Direct stays in its
  authorized group.
- Wrong model, ordinary schema error, authentication error, non-edge source,
  unselected account and non-candidate execution do not enter this recovery.
- All eligible accounts exhausted: no account is recycled or added to scope.

## Validation

Unit coverage: `TestCandidateEdgeModelRejection*`; existing candidate, failover,
protocol execution and error passthrough suites. Release acceptance repeats the
original traffic corpus and forces the rejected sticky account before retry.
Production preparation is authorized; traffic cutover remains prohibited until
the user's explicit review and approval.
