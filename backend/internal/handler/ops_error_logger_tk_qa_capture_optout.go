package handler

import "github.com/Wei-Shaw/sub2api/internal/service"

// QA-capture opt-out is a request-data switch, not a QA-pipeline switch.
//
// ops_error_logs is the second place a request can leave a persisted trace: it
// stores the upstream response body, error_body and sanitized detail, and it
// records requests that fail before auth completes, which never reach the QA
// capture at all. A user who turned QA capture off would otherwise still have
// those rows written, so the opt-out has to suppress this sink too.
//
// Attribution for pre-auth failures comes from the ops fallback API key that
// middleware stores before auth early-exits (disabled group/key, quota, IP
// limit); getOpsAPIKey falls back to it. That is what makes those requests
// attributable to a user, and therefore suppressible.
//
// Entries with no resolvable user (platform-wide failures, unauthenticated junk
// traffic) are deliberately unaffected: there is no opt-out to honor, and
// dropping them would blind operators to real incidents.
//
// The decision is stamped here, where the auth snapshot is in hand, and
// enforced once inside enqueueOpsErrorLog. Every sink funnels through that
// chokepoint, so a new error path cannot forget the check: entries built
// without the stamp simply keep the default (not suppressed), and the only way
// to honor the opt-out is to go through markOpsErrorLogUserOptOut.
func markOpsErrorLogUserOptOut(entry *service.OpsInsertErrorLogInput, apiKey *service.APIKey) {
	if entry == nil || apiKey == nil || apiKey.User == nil {
		return
	}
	if !apiKey.User.QACaptureEnabled {
		entry.SuppressForUserOptOut = true
	}
}
