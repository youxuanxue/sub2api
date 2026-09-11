//go:build integration

package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestUS054_MachineKeyPostgresPersistenceAndRevocation(t *testing.T) {
	ctx := context.Background()
	repo := NewSettingRepository(testEntTx(t).Client())
	svc := service.NewSettingService(repo, &config.Config{})
	meta, token, err := svc.CreateMachineAdminKey(ctx, 11, "integration", []string{"accounts:read"}, 1)
	require.NoError(t, err)
	other := service.NewSettingService(repo, &config.Config{})
	identity, err := other.AuthenticateMachineAdminKey(ctx, token)
	require.NoError(t, err)
	require.Equal(t, meta, identity)
	all, err := repo.GetAll(ctx)
	require.NoError(t, err)
	for _, value := range all {
		require.NotContains(t, value, token)
	}
	require.NoError(t, other.RevokeMachineAdminKey(ctx, meta.ID))
	_, err = svc.AuthenticateMachineAdminKey(ctx, token)
	require.ErrorIs(t, err, service.ErrInvalidMachineAdminKey)
}
