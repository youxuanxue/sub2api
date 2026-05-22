package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type accountRepoSumStub struct {
	AccountRepository
	sum int64
	err error
}

func (a *accountRepoSumStub) SumConcurrencyAnthropic(context.Context) (int64, error) {
	if a.err != nil {
		return 0, a.err
	}
	return a.sum, nil
}

type recordingUserRepoStub struct {
	UserRepository

	lastVal   int
	lastIDs   []int64
}

func (r *recordingUserRepoStub) BatchSetConcurrency(_ context.Context, userIDs []int64, value int) (int, error) {
	cp := append([]int64(nil), userIDs...)
	r.lastIDs = cp
	r.lastVal = value
	return len(userIDs), nil
}

func TestSyncAnthropicOperatorConcurrency_AppliesSum(t *testing.T) {
	ar := &accountRepoSumStub{sum: 41}
	ur := &recordingUserRepoStub{}
	require.NoError(t, SyncAnthropicOperatorConcurrency(context.Background(), ar, ur))
	require.Equal(t, 41, ur.lastVal)
	require.Equal(t, []int64{AnthropicOperatorConcurrencyUserID}, ur.lastIDs)
}

func TestSyncAnthropicOperatorConcurrency_NilRepo(t *testing.T) {
	require.Error(t,
		SyncAnthropicOperatorConcurrency(context.Background(), nil, &recordingUserRepoStub{}),
	)
	require.Error(t,
		SyncAnthropicOperatorConcurrency(context.Background(), &accountRepoSumStub{}, nil),
	)
}
