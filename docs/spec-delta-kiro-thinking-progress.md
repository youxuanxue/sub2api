# Kiro fixed waiting budget

## Background

The confirmed user 16 CogBench / DASFAA 2027 session contains one streaming
failure followed by five canceled non-streaming requests. Retained server logs
cover about 26 minutes, not the reported 90 minutes. They do not prove that
continuous upstream reasoning caused the initial stall, or identify the owner
of the approximately 360-second cancellations.

## Delta

Policy and user approval: [Candidate Eligibility SSOT](approved/candidate-eligibility-ssot.md#kiro-fixed-request-waiting-budget-2621-conversation-approval).
Replace progress renewal with independent Kiro ordinary/high fixed budgets.
Reuse the existing guard and request-scoped terminal error contract. Cover
streaming, complete non-streaming JSON, retries and prod→edge propagation.

## Scenarios and validation

- Fable/Opus without effort, enabled/adaptive thinking and explicit high effort:
  `TestKiroWaitBudgetPolicy`.
- Long reasoning completes inside the high budget:
  `TestKiroStreaming_ReasoningCompletesWithinHighBudget`.
- Continuous reasoning and non-streaming partial text do not renew the budget:
  `TestKiroWaitBudgetProgressDoesNotExtendDeadline`.
- Retry retains its deadline: `TestKiroWaitBudgetRetriesShareDeadline`.
- Mirror ping/partial JSON still times out:
  `TestKiroRelayBudgetBoundsBothResponseModes`.
- Caller cancellation releases reads without provider failure:
  `TestKiroNonStreamingCallerCancellation` and existing streaming cancel coverage.
- Only keepalive was sent, total budget exhausted: handler returns one terminal
  error without selecting another account:
  `TestGatewayHandlerMessages_KiroBudgetAfterKeepaliveStopsRequest`.

Real client “stream failure → non-stream fallback → exit” remains unverified;
client logs are unavailable. No production deploy is part of this change.
