# US-067 OAuth search history preserves tool permissions

- ID: US-067
- Priority: P1
- Title: Replay hosted-search history without changing caller tool permissions
- As a / I want / So that: As an OAuth Responses caller, I want the gateway to adapt hosted-search history so that my existing request can complete with its original tools and permissions.
- Trace: [Approved contract](../../../docs/approved/openai-search-history-tool-permissions.md); user 16 protection-failure investigation.
- Risk Focus:
  - 逻辑错误: Preserve auto/required/explicit/allowed selection and historical information in ordinary and Lite requests.
  - 行为回归: Forward passthrough and standard paths; API-key/compact exclusions; protection failures remain attributable upstream errors.
  - 安全问题: Never authorize a newly declared search tool, unknown references or unsupported namespace selectors; diagnostics exclude customer text and names.
  - 运行时问题: Normalization is idempotent; diagnostic emission is once per request and bounded even beyond the QA capture prefix. No persistent state or new concurrency owner.

## Acceptance Criteria

1. AC-001 (正向): Given search history without a declaration and supported active tools, when normalized or forwarded, then historical information and definitions survive, auto/required semantics survive, and only original tools remain selectable. Namespace search items become assistant text while other items remain raw.
2. AC-002 (负向): Given invalid/undeclared references, unsupported namespace selection or server-held history, when normalized, then the body remains unchanged; explicit none still permits the existing no-execution repair.
3. AC-003 (回归): Given repeated normalization, API-key/compact traffic or a protection failure, then normalization is idempotent, exclusions remain intact and account retries do not amplify this error or turn it into client 400.
4. AC-004 (安全/运行时): Given a protection failure with a large request, then structural evidence inspects beyond the QA prefix, excludes customer content and names, and is emitted at most once for that request.

## Assertions

- AC-001: Exact allowed-tool set/mode, raw declaration/history equality, outgoing Forward body and Lite trigger placement.
- AC-002: Byte equality for unsupported inputs; namespace identity retained; newly injected search absent from executable references.
- AC-001/AC-003: Namespace text preserves complete search JSON, reasoning, citations, unknown fields and numeric precision; leaves tools/choice intact and is idempotent.
- AC-003: Byte equality after a second normalization; compatibility exclusions; one upstream attempt and upstream-attributed 5xx.
- AC-004: Exact history/namespace counts, bounded JSON, no private text, one diagnostic event and no event for other errors.

## Linked Tests

- AC-004: `backend/internal/handler/us067_oauth_search_history_test.go`::`TestUS067_NativeResponsesRetainsFullDiagnosticInput`
- Run: `cd backend && go test -p 1 -tags=unit ./internal/handler -run TestUS067_ -count=1`

- AC-001/AC-003: `backend/internal/service/us067_oauth_search_history_test.go`::`TestUS067_PreservesActiveToolSelection`
- AC-001: `backend/internal/service/us067_oauth_search_history_test.go`::`TestUS067_ForwardPreservesActiveTools`
- AC-002: `backend/internal/service/us067_oauth_search_history_test.go`::`TestUS067_NeverAuthorizesUndeclaredTools`
- AC-002: `backend/internal/service/us067_oauth_search_history_test.go`::`TestUS067_NamespaceRemainsNative`
- AC-001/AC-003: `backend/internal/service/us067_oauth_search_history_test.go`::`TestUS067_NamespaceHistoryTextPreservesContextAndPermissions`
- AC-001: `backend/internal/service/us067_oauth_search_history_test.go`::`TestUS067_ForwardNamespaceHistoryText`
- AC-002: `backend/internal/service/us067_oauth_search_history_test.go`::`TestUS067_NamespaceHistoryTextRejectsUnsupportedShapes`
- AC-003: `backend/internal/service/openai_oauth_web_search_history_tk_test.go`::`TestOAuthWebSearchHistoryCompatibilityScope`
- AC-003: `backend/internal/service/openai_oauth_web_search_history_tk_test.go`::`TestResponseProtectionStopsAccountAmplificationWithoutClient400`
- AC-004: `backend/internal/service/us067_oauth_search_history_test.go`::`TestUS067_DiagnosticExcludesCustomerContent`
- AC-004: `backend/internal/service/us067_oauth_search_history_test.go`::`TestUS067_DiagnosticIsBoundedAndEmittedOnce`
- AC-004: `backend/internal/service/us067_oauth_search_history_test.go`::`TestUS067_DiagnosticInspectsBeyondQAPrefix`
- Run: `cd backend && go test -p 1 -tags=unit ./internal/service -run 'Test(US067_|OAuthWebSearchHistory|ForwardOAuthWebSearchHistory|ResponseProtection)' -count=1`
- Regression: `cd backend && go test -p 1 -tags=unit ./internal/service ./internal/handler`

## Evidence

- Synthetic live probe evidence and limitations: [approved contract](../../../docs/approved/openai-search-history-tool-permissions.md#evidence-and-limitations).
- The historical customer payload was truncated; synthetic success does not prove that the customer's exact session has recovered.
- Deployment and live verification of the automatic repair remain pending. These are service/unit and synthetic gateway checks, not browser e2e.

## Status

- [x] InTest
