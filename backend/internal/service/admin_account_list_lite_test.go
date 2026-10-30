//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/pagination"
	"github.com/stretchr/testify/require"
)

type liteListAccountRepo struct {
	AccountRepository
	liteCalls int
	fullCalls int
}

func (r *liteListAccountRepo) ListWithFiltersLite(context.Context, pagination.PaginationParams, string, string, string, string, int64, string, int, ...AccountListOptions) ([]Account, *pagination.PaginationResult, error) {
	r.liteCalls++
	return []Account{{ID: 7, Name: "lite"}}, &pagination.PaginationResult{Total: 1}, nil
}

func (r *liteListAccountRepo) ListWithFilters(context.Context, pagination.PaginationParams, string, string, string, string, int64, string, int, ...AccountListOptions) ([]Account, *pagination.PaginationResult, error) {
	r.fullCalls++
	return []Account{{ID: 8, Name: "full"}}, &pagination.PaginationResult{Total: 1}, nil
}

func TestListAccountsLiteSkipsFullGroupHydration(t *testing.T) {
	repo := &liteListAccountRepo{}
	svc := &adminServiceImpl{accountRepo: repo}

	accounts, total, err := svc.ListAccountsLite(context.Background(), 1, 100, "", "", "", "", 0, "", "name", "asc", 0)
	require.NoError(t, err)
	require.Equal(t, int64(1), total)
	require.Equal(t, int64(7), accounts[0].ID)
	require.Equal(t, 1, repo.liteCalls)
	require.Zero(t, repo.fullCalls)

	accounts, _, err = svc.ListAccounts(context.Background(), 1, 100, "", "", "", "", 0, "", "name", "asc", 0)
	require.NoError(t, err)
	require.Equal(t, int64(8), accounts[0].ID)
	require.Equal(t, 1, repo.fullCalls)
}
