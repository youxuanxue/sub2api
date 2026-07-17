//go:build unit

package service

import (
	"context"
	"testing"

	kiro "github.com/Wei-Shaw/sub2api/internal/pkg/kiro"
	"github.com/stretchr/testify/require"
)

func TestCreateAccount_DefaultsKiroPriorityWhenOmitted(t *testing.T) {
	ctx := context.Background()
	repo := newSparkShadowRepoStub()
	svc := &adminServiceImpl{accountRepo: repo}

	account, err := svc.CreateAccount(ctx, &CreateAccountInput{
		Name:                 "kiro-default-priority",
		Platform:             PlatformKiro,
		Type:                 AccountTypeOAuth,
		Credentials:          map[string]any{"region": "us-east-1"},
		Priority:             0,
		SkipDefaultGroupBind: true,
	})
	require.NoError(t, err)
	require.Equal(t, kiro.DefaultKiroAccountPriority, account.Priority)
	require.Equal(t, kiro.DefaultKiroAccountPriority, repo.accounts[account.ID].Priority)
}
