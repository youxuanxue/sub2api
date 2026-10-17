package service

import "context"

// OpenAISaturationCounterCache stores rolling native OpenAI account capacity failures.
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
