package service

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Settlement owns a handed-off hold, so every exit from RecordUsage must leave
// usage_holds clean. These pin the non-consuming exits found in the production
// hold-leak investigation: a bill the dedup layer refuses (Apply returns before
// applying effects, so tkConsumeBalanceHoldInTx never runs) and an early return
// before the billing call at all.

type holdLeakBillingRepo struct {
	UsageBillingRepository
	mu       sync.Mutex
	applied  bool
	applyErr error
	consumed []string
	released []string
}

func (r *holdLeakBillingRepo) Apply(_ context.Context, cmd *UsageBillingCommand) (*UsageBillingApplyResult, error) {
	if r.applyErr != nil {
		return nil, r.applyErr
	}
	if !r.applied {
		// Dedup/fingerprint rejection: no effects applied, so no hold consumed.
		return &UsageBillingApplyResult{Applied: false}, nil
	}
	r.mu.Lock()
	r.consumed = append(r.consumed, cmd.TkHoldRequestID)
	r.mu.Unlock()
	return &UsageBillingApplyResult{Applied: true}, nil
}

func (r *holdLeakBillingRepo) ReserveBalanceHold(context.Context, *HoldCommand) (bool, error) {
	return true, nil
}

func (r *holdLeakBillingRepo) ReleaseBalanceHold(_ context.Context, requestID string) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, requestID)
	return true, nil
}

func (r *holdLeakBillingRepo) ReleaseExpiredBalanceHolds(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

func (r *holdLeakBillingRepo) releasedKeys() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.released...)
}

func newHoldLeakService(t *testing.T, repo *holdLeakBillingRepo) *OpenAIGatewayService {
	t.Helper()
	svc := newOpenAIRecordUsageServiceForTest(
		&openAIRecordUsageLogRepoStub{inserted: true},
		&openAIRecordUsageUserRepoStub{},
		&openAIRecordUsageSubRepoStub{},
		nil,
	)
	svc.usageBillingRepo = repo
	return svc
}

func holdLeakUsageInput(holdRequestID string) *OpenAIRecordUsageInput {
	return &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "upstream-" + holdRequestID,
			Model:     "gpt-6-astra",
			Usage:     OpenAIUsage{InputTokens: 100, OutputTokens: 50},
			Duration:  time.Second,
		},
		APIKey:          &APIKey{ID: 501, Quota: 100},
		User:            &User{ID: 16},
		Account:         &Account{ID: 701},
		TkHoldRequestID: holdRequestID,
	}
}

// The incident's structural leak: the bill is refused by the dedup claim, so
// settlement never consumes the hold. Before the fix the row survived to the
// 30-minute reconciler TTL, holding real balance against admission.
func TestRecordUsage_ReleasesHoldWhenSettlementDoesNotApply(t *testing.T) {
	repo := &holdLeakBillingRepo{applied: false}
	svc := newHoldLeakService(t, repo)

	require.NoError(t, svc.RecordUsage(context.Background(), holdLeakUsageInput("local:dedup-refused")))
	require.Equal(t, []string{"local:dedup-refused"}, repo.releasedKeys(),
		"a bill the dedup layer refuses must not strand its hold")
}

func TestRecordUsage_ReleasesHoldOnBillingError(t *testing.T) {
	repo := &holdLeakBillingRepo{applyErr: errors.New("database unavailable")}
	svc := newHoldLeakService(t, repo)

	require.Error(t, svc.RecordUsage(context.Background(), holdLeakUsageInput("local:apply-error")))
	require.Equal(t, []string{"local:apply-error"}, repo.releasedKeys(),
		"a failed settlement must not strand its hold")
}

func TestRecordUsage_ReleasesHoldOnEarlyReturn(t *testing.T) {
	repo := &holdLeakBillingRepo{applied: true}
	svc := newHoldLeakService(t, repo)

	// nil Result returns before any billing work; the hold is already handed off.
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		TkHoldRequestID: "local:nil-result",
	})
	require.Error(t, err)
	require.Equal(t, []string{"local:nil-result"}, repo.releasedKeys(),
		"an early return must not strand its hold")
}

// The compensating release must never become a second refund on the happy path.
// Both the in-transaction consume and the release are DELETE ... RETURNING
// guarded, so the compensation is a no-op once settlement consumed the row —
// this pins that the normal path still consumes exactly once.
func TestRecordUsage_ConsumesHoldExactlyOnceOnSuccess(t *testing.T) {
	repo := &holdLeakBillingRepo{applied: true}
	svc := newHoldLeakService(t, repo)

	require.NoError(t, svc.RecordUsage(context.Background(), holdLeakUsageInput("local:settled")))

	repo.mu.Lock()
	consumed := append([]string(nil), repo.consumed...)
	repo.mu.Unlock()
	require.Equal(t, []string{"local:settled"}, consumed,
		"settlement must consume the hold in its own transaction")
}

func TestRecordUsage_NoHoldKeyNeedsNoRelease(t *testing.T) {
	repo := &holdLeakBillingRepo{applied: true}
	svc := newHoldLeakService(t, repo)

	require.NoError(t, svc.RecordUsage(context.Background(), holdLeakUsageInput("")))
	require.Empty(t, repo.releasedKeys(), "an ungated request has no hold to release")
}
