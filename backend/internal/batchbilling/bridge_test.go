package batchbilling

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

type counterStub struct {
	owner  Owner
	counts ActiveCounts
}

func (s counterStub) ActiveCounts(_ context.Context, owner Owner) (ActiveCounts, error) {
	if !owner.Same(s.owner) {
		return ActiveCounts{}, errors.New("unscoped owner")
	}
	return s.counts, nil
}

func TestAdmissionEnforcesOwnerAndLimits(t *testing.T) {
	c := &AdmissionController{
		Counter: counterStub{owner: Owner{UserID: 7, APIKeyID: 9}, counts: ActiveCounts{Batches: 1, Items: 2}},
		Limits:  Limits{MaxItems: 10, MaxInputBytes: 1000, MaxActiveBatches: 2, MaxActiveItems: 10},
	}
	got, err := c.Admit(context.Background(), AdmissionRequest{Owner: Owner{UserID: 7, APIKeyID: 9}, BatchID: " b1 ", ItemCount: 3, InputBytes: 12})
	require.NoError(t, err)
	require.Equal(t, "b1", got.BatchID)

	_, err = c.Admit(context.Background(), AdmissionRequest{Owner: Owner{UserID: 7, APIKeyID: 9}, BatchID: "b2", ItemCount: 11})
	require.ErrorIs(t, err, ErrAdmissionDenied)
	_, err = c.Admit(context.Background(), AdmissionRequest{Owner: Owner{UserID: 7, APIKeyID: 9}, BatchID: "b3", ItemCount: 3, InputBytes: 1001})
	require.ErrorIs(t, err, ErrAdmissionDenied)
	_, err = c.Admit(context.Background(), AdmissionRequest{Owner: Owner{UserID: 8, APIKeyID: 9}, BatchID: "b4", ItemCount: 1})
	require.Error(t, err)
}

type ownerReaderStub struct{ owner Owner }

func (s ownerReaderStub) BatchOwner(context.Context, string) (Owner, error) { return s.owner, nil }

func TestAccessGuardCollapsesCrossOwnerAndMissingToNotFound(t *testing.T) {
	g := AccessGuard{Reader: ownerReaderStub{owner: Owner{UserID: 10, APIKeyID: 20}}}
	require.NoError(t, g.Require(context.Background(), Owner{UserID: 10, APIKeyID: 20}, "batch-1"))
	require.ErrorIs(t, g.Require(context.Background(), Owner{UserID: 11, APIKeyID: 20}, "batch-1"), ErrOwnerNotFound)
	require.ErrorIs(t, g.Require(context.Background(), Owner{UserID: 10, APIKeyID: 0}, "batch-1"), nil)
	// Invalid owner and empty IDs never reach persistence and expose no detail.
	require.ErrorIs(t, g.Require(context.Background(), Owner{}, "batch-1"), ErrOwnerNotFound)
	require.ErrorIs(t, g.Require(context.Background(), Owner{UserID: 10, APIKeyID: 20}, ""), ErrOwnerNotFound)
}

type holdStub struct {
	reserved []string
	released []string
}

func (s *holdStub) Reserve(_ context.Context, _ Owner, key string, amount float64) (bool, error) {
	if !ValidAmount(amount) || amount <= 0 {
		return false, errors.New("bad amount")
	}
	s.reserved = append(s.reserved, key)
	return true, nil
}
func (s *holdStub) Release(_ context.Context, _ Owner, key string) (bool, error) {
	s.released = append(s.released, key)
	return true, nil
}

type settleStub struct {
	keys []string
}

func (s *settleStub) Settle(_ context.Context, req SettlementRequest, holdKey string) (SettlementResult, error) {
	if !req.Usage.Valid() {
		return SettlementResult{}, errors.New("bad usage")
	}
	s.keys = append(s.keys, holdKey)
	return SettlementResult{Applied: true}, nil
}

func TestBridgeUsesStableHoldAndSettlementKeys(t *testing.T) {
	holds := &holdStub{}
	settle := &settleStub{}
	b := &Bridge{Holds: holds, Settlement: settle}
	owner := Owner{UserID: 1, APIKeyID: 2}
	ref := ItemRef{BatchID: "batch-1", ItemID: "item-9", Attempt: 3}

	reserved, err := b.ReserveItem(context.Background(), owner, ref, 1.25)
	require.NoError(t, err)
	require.True(t, reserved)
	got, err := b.SettleItem(context.Background(), SettlementRequest{Owner: owner, Ref: ref, Usage: UsageObservation{InputTokens: 3, OutputTokens: 4, TotalTokens: 7}})
	require.NoError(t, err)
	require.True(t, got.Applied)
	require.Equal(t, []string{"hold/batch/batch-1/item/item-9/attempt/3"}, holds.reserved)
	require.Equal(t, holds.reserved, settle.keys)

	require.Equal(t, "batch/batch-1/item/item-9/attempt/3", mustSettlementKey(t, ref))
	_, err = b.ReserveItem(context.Background(), owner, ref, 0)
	require.ErrorIs(t, err, ErrInvalidRequest)
}

func mustSettlementKey(t *testing.T, ref ItemRef) string {
	t.Helper()
	key, err := SettlementKey(ref)
	require.NoError(t, err)
	return key
}

func TestUnknownReconciliationRequiresObservedUsageAndNeverRetries(t *testing.T) {
	holds := &holdStub{}
	settle := &settleStub{}
	r := &Reconciler{Holds: holds, Settlement: settle}
	owner := Owner{UserID: 1, APIKeyID: 2}
	ref := ItemRef{BatchID: "b", ItemID: "i", Attempt: 1}
	req := SettlementRequest{Owner: owner, Ref: ref}

	_, err := r.ResolveUnknown(context.Background(), req, UnknownDecision{Ref: ref})
	require.ErrorIs(t, err, ErrUnknownUnresolved)
	_, err = r.ResolveUnknown(context.Background(), req, UnknownDecision{Ref: ref, Outcome: OutcomeSettled})
	require.ErrorIs(t, err, ErrUnknownUnresolved)
	_, err = r.ResolveUnknown(context.Background(), req, UnknownDecision{Ref: ref, Outcome: OutcomeSettled, Usage: UsageObservation{InputTokens: 2, OutputTokens: 1, TotalTokens: 3}, UsageObserved: true})
	require.NoError(t, err)
	require.Len(t, settle.keys, 1)
	_, err = r.ResolveUnknown(context.Background(), req, UnknownDecision{Ref: ref, Outcome: OutcomeReleased, UsageObserved: true})
	require.ErrorIs(t, err, ErrUnknownUnresolved)
	_, err = r.ResolveUnknown(context.Background(), req, UnknownDecision{Ref: ref, Outcome: OutcomeReleased})
	require.NoError(t, err)
	require.Equal(t, []string{"hold/batch/b/item/i/attempt/1"}, holds.released)
	_, err = r.ResolveUnknown(context.Background(), req, UnknownDecision{Ref: ItemRef{BatchID: "b", ItemID: "other", Attempt: 1}, Outcome: OutcomeReleased})
	require.ErrorIs(t, err, ErrInvalidRequest)
}

func TestAuditFieldsRejectSecretsAndRemainDeterministic(t *testing.T) {
	fields, err := SanitizeAuditFields(map[string]string{
		"batch_id":      "b1",
		"owner_user_id": "42",
		"outcome":       "unknown",
	})
	require.NoError(t, err)
	require.Equal(t, []string{"batch_id", "outcome", "owner_user_id"}, SortedAuditKeys(fields))
	_, err = SanitizeAuditFields(map[string]string{"prompt": "secret"})
	require.ErrorIs(t, err, ErrInvalidAudit)
	_, err = SanitizeAuditFields(map[string]string{"owner_user_id": "42\nsecret"})
	require.ErrorIs(t, err, ErrInvalidAudit)
	_, err = SanitizeAuditFields(map[string]string{"owner_user_id": "user-42"})
	require.ErrorIs(t, err, ErrInvalidAudit)
}
