package service

import "fmt"

// openAICompatErrorPlatformLabel returns the platform identifier to embed in
// "no available accounts" error messages produced by the OpenAI-compat
// scheduler.
//
// Background (docs/bugs/2026-04-23-newapi-fifth-platform-audit.md, P1-2):
// the OpenAI-compat scheduling pool now carries both `openai` and `newapi`
// accounts (see docs/approved/newapi-as-fifth-platform.md). Hard-coded
// "no available OpenAI accounts" error text caused operator confusion when
// the failing group was actually `newapi`. We surface req.GroupPlatform
// verbatim, falling back to PlatformOpenAI for the legacy "no group / empty
// platform" case so existing tests / log greps that expect "openai" continue
// to work for openai-shaped requests.
//
// Kept in a TK-only companion file so future upstream merges of
// openai_account_scheduler.go do not collide on this branding choice.
func openAICompatErrorPlatformLabel(groupPlatform string) string {
	if groupPlatform == "" {
		return PlatformOpenAI
	}
	return groupPlatform
}

// openAICompatNoCandidateErr is the single exit for the OpenAI-compat scheduler's
// "candidate filter emptied the schedulable pool" branch (selectByLoadBalance,
// len(filtered)==0). It mirrors the anthropic path's tkWrapSelectionFailure
// (gateway_service_tk_unsupported_model.go): when EVERY schedulable account in
// the matched pool was rejected purely because it does not serve the requested
// model NAME, the failure is a CLIENT error (wrong/legacy model id), not capacity
// — return service.ErrUnsupportedModel so the handler maps it to HTTP 400
// invalid_request_error (kept out of upstream_error_rate). Otherwise the original
// empty-pool errors are preserved verbatim (429 fast-fail / platform label).
//
// Prod 2026-06-13: a client requesting unservable newapi names (deepseek-chat,
// qwen-max — the matched pools only mapped deepseek-v4-* / qwen3.7-*) produced
// empty-pool 429s (account_id=null) that read as a capacity signal and fired ops
// alerts. This converges the openai/newapi pools with the anthropic 400 behavior.
//
// Only the len(filtered)==0 branch routes here. The len(accounts)==0 branch (no
// schedulable account seen at all → no model evidence) and the post-load-balance
// "couldn't acquire a slot" branches stay 429: those are genuine capacity gaps.
func (s *defaultOpenAIAccountScheduler) openAICompatNoCandidateErr(req OpenAIAccountScheduleRequest, accounts []Account) error {
	if req.RequestedModel != "" {
		stats := collectOpenAICompatSelectionFailureStats(accounts, req)
		if tkSelectionFailedDueToUnsupportedModel(stats) {
			return fmt.Errorf("%w: %s (%s)", ErrUnsupportedModel, req.RequestedModel, summarizeSelectionFailureStats(stats))
		}
	}
	if req.GroupPlatform != "" && req.GroupPlatform != PlatformOpenAI {
		return fmt.Errorf("no available accounts for platform %q", openAICompatErrorPlatformLabel(req.GroupPlatform))
	}
	return noAvailableOpenAISelectionError(req.RequestedModel, false, req.GroupPlatform)
}

// collectOpenAICompatSelectionFailureStats categorizes each schedulable account
// that survived to the candidate filter into the shared selectionFailureStats so
// tkSelectionFailedDueToUnsupportedModel can decide "purely unsupported model" vs
// "transient capacity". It deliberately only distinguishes the model-support axis
// (Account.IsModelSupported — the same check isAccountRequestCompatible applies):
//
//   - account does NOT support the model name  → ModelUnsupported (evidence the
//     name is unserved);
//   - account supports the model but was excluded or filtered for any other
//     reason (runtime block, capability, transport, upstream restriction) →
//     Unschedulable, which SUPPRESSES the 400: the model IS served, just not
//     selectable right now, so the client should get the 429 capacity hint, not a
//     misleading "Unsupported model".
//
// Eligible is 0 by construction here (any fully-eligible account would be in
// `filtered`); ModelRateLimited is not tracked on this path (account-level rate
// limits are filtered out of the schedulable list upstream). The net predicate
// therefore fires only when at least one account is model-unsupported AND none
// supports the model.
func collectOpenAICompatSelectionFailureStats(accounts []Account, req OpenAIAccountScheduleRequest) selectionFailureStats {
	stats := selectionFailureStats{Total: len(accounts)}
	for i := range accounts {
		acc := &accounts[i]
		if req.ExcludedIDs != nil {
			if _, excluded := req.ExcludedIDs[acc.ID]; excluded {
				// A previously-attempted account may well serve the model; never
				// let an excluded account contribute "unsupported" evidence.
				stats.Unschedulable++
				continue
			}
		}
		if req.RequestedModel != "" && !acc.IsModelSupported(req.RequestedModel) {
			stats.ModelUnsupported++
			stats.SampleMappingIDs = appendSelectionFailureSampleID(stats.SampleMappingIDs, acc.ID)
			continue
		}
		stats.Unschedulable++
	}
	return stats
}
