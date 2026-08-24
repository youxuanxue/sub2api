# Anthropic Buffered Stream Failure Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make all four buffered Anthropic SSE assembly paths fail over before content, preserve and mark partial content, and return accurate final error contracts.

**Architecture:** Keep parsing, completion-state classification, Ops event construction, and client error mapping in `gateway_anthropic_buffered_error_tk.go`. Pass the selected account into the four buffered assemblers so the shared owner can write complete attempt identity. Return `UpstreamFailoverError` before any client response is committed; let existing handler loops perform health processing, account switching, and exhausted-response writing.

**Tech Stack:** Go 1.26.6, Gin, `bufio.Scanner`, existing `UpstreamFailoverError`, existing Ops context and sentinel checks.

**Spec:** `docs/approved/anthropic-buffered-stream-failure-contract.md`

## Global Constraints

- Do not retry after a content block has been assembled.
- Do not write a response in service code before returning a failover error.
- Preserve the partial HTTP 200 response and mark it towards SLA.
- Every new Ops upstream event includes platform, account ID, and account name.
- Raw persisted detail remains gated by `log_upstream_error_body`.
- No new feature flag, schema, route, or frontend contract.

---

### Task 1: Pin failure classification with RED tests

**Files:**
- Modify: `backend/internal/service/gateway_anthropic_buffered_error_tk_test.go`
- Modify: `backend/internal/service/openai_gateway_anthropic_native_pump_test.go`

**Interfaces:**
- Consumes: existing four buffered assembly functions and `UpstreamFailoverError`.
- Produces: failing `TestUS047_*` regression tests for no-content failover, partial-content preservation, incomplete EOF, timeout, identity, and error type mapping.

- [x] **Step 1: Write failing tests**

Add tests that pass a real `Account{ID, Name, Platform}`, assert `errors.As` for no-content failures, assert recorder remains unwritten before failover exhaustion, and assert partial responses stay HTTP 200 with one SLA-counted stream failure.

- [x] **Step 2: Run tests to verify RED**

Run: `cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/service -run 'TestUS047_' -count=1`

Expected: FAIL because buffered handlers currently write errors directly, treat `message_start` as success, omit account identity, and do not classify incomplete EOF/idle by content availability.

### Task 2: Return failover errors before content and preserve partial content

**Files:**
- Modify: `backend/internal/service/gateway_anthropic_buffered_error_tk.go`
- Modify: `backend/internal/service/gateway_forward_as_chat_completions.go`
- Modify: `backend/internal/service/gateway_forward_as_responses.go`
- Modify: `backend/internal/service/openai_gateway_chat_completions_anthropic_native.go`
- Modify: `backend/internal/service/openai_gateway_responses_anthropic_native.go`
- Modify: `backend/internal/service/gateway_service.go`

**Interfaces:**
- Consumes: `Account`, `UpstreamFailoverError`, `MarkOpsStreamFailure`, service-specific account-health helpers.
- Produces: shared normalized failure state, complete Ops events, `ClientErrorType` on failover errors, and four account-aware buffered assemblers.

- [x] **Step 1: Implement the minimal shared state**

Track terminal completion separately from `finalResp`, retain a bounded in-memory error payload, and classify failure by whether at least one content block exists.

- [x] **Step 2: Implement no-content failover**

Build an `UpstreamFailoverError` carrying semantic upstream status, response body/headers, `ClientStatusCode`, `ClientErrorType`, and `ClientMessage`. Run the same account-health side effects used by ordinary upstream failures. Do not write to `gin.Context` response output.

- [x] **Step 3: Implement partial-content truncation**

For error, timeout, read error, or incomplete EOF after a content block, record `stream_truncated`, call `MarkOpsStreamFailure`, and continue the existing response conversion without replay.

- [x] **Step 4: Run focused tests to verify GREEN**

Run: `cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/service -run 'TestUS047_|TestTKAnthropicBufferedError_|TestCCBufferedFromNativeAnthropic_' -count=1`

Expected: PASS.

### Task 3: Preserve final error type at failover exhaustion

**Files:**
- Modify: `backend/internal/handler/gateway_handler.go`
- Modify: `backend/internal/handler/gateway_handler_responses.go`
- Modify: `backend/internal/handler/openai_gateway_handler.go`
- Test: `backend/internal/handler/gateway_anthropic_buffered_failover_tk_test.go`

**Interfaces:**
- Consumes: `UpstreamFailoverError.ClientErrorType`.
- Produces: handler exhausted paths that use the supplied error type when `ClientStatusCode` is set, falling back to existing mappings for all old callers.

- [x] **Step 1: Write handler regression tests**

Assert 404 emits `not_found_error`, 413 emits `request_too_large`, and existing errors without `ClientErrorType` preserve their previous mapping.

- [x] **Step 2: Run tests to verify RED**

Run: `cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/handler -run 'TestUS047_' -count=1`

Expected: FAIL because exhausted handlers currently hard-code `api_error` / `server_error` for custom client statuses.

- [x] **Step 3: Implement minimal handler support**

Use trimmed `ClientErrorType` when present; retain each handler's current default when absent.

- [x] **Step 4: Run handler tests to verify GREEN**

Run: `cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/handler -run 'TestUS047_|Test.*FailoverExhausted' -count=1`

Expected: PASS.

### Task 4: Update sentinel, PR evidence, and verify the branch

**Files:**
- Modify: `scripts/sentinels/gateway-tk.json`
- Modify: PR #1805 body through `gh pr edit` only after local commit and push approval.

**Interfaces:**
- Consumes: final shared helper and four call-site signatures.
- Produces: upstream-merge mechanical anchors and fresh review evidence.

- [x] **Step 1: Update sentinel literals**

Pin the account-aware helper calls, no-content failover construction, partial truncation hook, and focused `TestUS047_*` tests.

- [x] **Step 2: Run focused and package tests**

Run: `cd backend && GOTOOLCHAIN=go1.26.6 go test -tags=unit ./internal/service/... ./internal/handler/... ./internal/pkg/apicompat/... -count=1`

- [x] **Step 3: Run full mechanical gate**

Run: `GOTOOLCHAIN=go1.26.6 PREFLIGHT_BASE=origin/main bash scripts/preflight.sh`

- [x] **Step 4: Re-run xj-review pipeline**

Run `pipeline find`, verify every R-001..R-007 outcome, then run `adversarial-verify` and `dedup`. The result must contain zero medium-or-higher findings.

- [ ] **Step 5: Commit locally**

Commit with a high-risk approval anchor and `no-web-impact`, then run the xj-review push gate. Do not push when the gate returns `verdict=halt`; present the committed diff for explicit approval.
