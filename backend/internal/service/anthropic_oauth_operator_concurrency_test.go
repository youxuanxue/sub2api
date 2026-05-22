//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type accountRepoSumStub struct {
	accountRepoStub
	sum int64
}

func (a *accountRepoSumStub) SumConcurrencyAnthropicOAuth(context.Context) (int64, error) {
	return a.sum, nil
}

type recordingUserRepoStub struct {
	userRepoStub
	lastIDs []int64
	lastVal int
	calls   int
}

func (r *recordingUserRepoStub) BatchSetConcurrency(ctx context.Context, ids []int64, val int) (int, error) {
	r.calls++
	r.lastIDs = append([]int64{}, ids...)
	r.lastVal = val
	return 1, nil
}

func TestSyncAnthropicOAuthOperatorConcurrency_AppliesSum(t *testing.T) {
	ar := &accountRepoSumStub{sum: 17}
	ur := &recordingUserRepoStub{}
	require.NoError(t, SyncAnthropicOAuthOperatorConcurrency(context.Background(), ar, ur))
	require.Equal(t, 17, ur.lastVal)
	require.Equal(t, []int64{AnthropicOAuthOperatorConcurrencyUserID}, ur.lastIDs)
	require.Equal(t, 1, ur.calls)
}

func TestSyncAnthropicOAuthOperatorConcurrency_NilRepo(t *testing.T) {
	require.ErrorContains(t,
		SyncAnthropicOAuthOperatorConcurrency(context.Background(), nil, &recordingUserRepoStub{}),
		"nil repository",
	)
}
