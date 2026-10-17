package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUS066_RetiredSupplierCannotEnterRepositoryOrScheduler(t *testing.T) {
	account := service.Account{Status: service.StatusActive, Schedulable: true,
		Credentials: map[string]any{"cursor_wire_models": map[string]any{"old": "old"}}}
	// The scheduler projection drops retired wire parameters, but must retain
	// ineligibility rather than turning an old mirror into ordinary API supply.
	projected := buildSchedulerMetadataAccount(account)
	require.False(t, projected.IsSchedulable())
	// Both writes fail before any database access, including generic imports.
	repo := &accountRepository{}
	require.ErrorIs(t, repo.Create(context.Background(), &account), service.ErrSupplierRetired)
	require.ErrorIs(t, repo.Update(context.Background(), &account), service.ErrSupplierRetired)
}
