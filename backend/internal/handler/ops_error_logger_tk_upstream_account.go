package handler

import "github.com/Wei-Shaw/sub2api/internal/service"

// TK: attribute a failed request's ops row to the account that produced the
// recorded upstream error, not to whichever account was selected last.
//
// opsAccountIDKey holds the account selected for the final attempt. When a
// failover chain ends because the client disconnected (499) or because the
// last hop produced no upstream event, that account never returned an error,
// yet upstream_status_code / upstream_error_message come from the chain's last
// event. Prod 2026-11-06: 170 of 507 499 rows in 7 days carried a Volcengine
// Agent Plan quota 429 under an account that served nothing; per-account
// triage then blamed healthy accounts and missed the exhausted ones.
//
// The recovered-upstream path (logOpsRecoveredUpstream) already attributes to
// the last event's account; this makes the failure path consistent. Only
// events that carry an account move the attribution; account-less chains keep
// the selected account.
func tkAttributeOpsUpstreamAccount(entry *service.OpsInsertErrorLogInput) {
	if entry == nil {
		return
	}
	for i := len(entry.UpstreamErrors) - 1; i >= 0; i-- {
		event := entry.UpstreamErrors[i]
		if event == nil {
			continue
		}
		if event.AccountID > 0 {
			accountID := event.AccountID
			entry.AccountID = &accountID
		}
		return
	}
}
