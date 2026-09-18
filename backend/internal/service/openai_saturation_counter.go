package service

import "context"

// OpenAISaturationCounterCache tracks a rolling-window count of recent capacity
// pressure per OpenAI account id. Writers include prod edge-mirror stubs
// (downstream-empty envelopes and sanitized edge capacity failures) and edge
// OpenAI OAuth / setup-token accounts (native upstream overloaded or 503
// temporarily unavailable). Same semantics as AnthropicSaturationCounterCache —
// a routing preference, not a cooldown. See
// ratelimit_service_tk_openai_saturation.go and openai_capacity_saturation_tk.go.
type OpenAISaturationCounterCache interface {
	IncrementSaturation(ctx context.Context, accountID int64, windowSeconds int) (count int64, err error)
	GetSaturationBatch(ctx context.Context, accountIDs []int64, windowSeconds int) (map[int64]int64, error)
}

// CandidateFailureScope keeps a supplier's model failure out of unrelated models.
type CandidateFailureScope struct {
	AccountID int64
	Model     string
}

type CandidateFailureCounter interface {
	IncrementCandidateFailure(context.Context, CandidateFailureScope, int) (int64, error)
	GetCandidateFailures(context.Context, []CandidateFailureScope) (map[CandidateFailureScope]int64, error)
}
