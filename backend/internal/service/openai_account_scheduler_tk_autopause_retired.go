package service

// tkOpenAIAutoPauseRetired reports whether the upstream codex usage-window
// "auto-pause by quota" decision is retired.
//
// It is retired in favour of the window-sched tri-state guard
// (openai_account_scheduler_tk_window_sched.go, PR #899), which is the single
// outward window-avoidance mechanism for the OpenAI/GPT line. Auto-pause was a
// binary hard-exclude on the SAME codex 5h/7d signal, so keeping both would mean
// two overlapping percent-based knobs for operators to reason about.
//
// We DISABLE rather than delete (§5.x deletion discipline): shouldAutoPause
// OpenAIAccountByQuota and its helpers are upstream-owned, so deleting them would
// cause recurring merge conflicts and lose upstream tests. Crucially, the codex
// signal CAPTURE those helpers share (resolveOpenAIQuotaUtilization,
// codex_*_used_percent persistence) is left fully intact — the #899 window guard
// depends on it. This gate only short-circuits the auto-pause DECISION, so any
// leftover per-account / global auto_pause thresholds can no longer fire either.
//
// Implemented as a function (not a const/var) so the gated upstream body is never
// flagged as unreachable/always-false by the linter, and so re-enabling auto-pause
// later is a one-line change here.
func tkOpenAIAutoPauseRetired() bool { return true }
