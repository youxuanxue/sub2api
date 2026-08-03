//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	newapifusion "github.com/Wei-Shaw/sub2api/internal/integration/newapi"
	"github.com/stretchr/testify/require"
)

type updateAccountCredsRepoStub struct {
	mockAccountRepoForGemini
	account     *Account
	updateCalls int
}

func (r *updateAccountCredsRepoStub) GetByID(ctx context.Context, id int64) (*Account, error) {
	return r.account, nil
}

func (r *updateAccountCredsRepoStub) Update(ctx context.Context, account *Account) error {
	r.updateCalls++
	r.account = account
	return nil
}

func TestUpdateAccount_PreservesSensitiveCredsWhenIncomingOmits(t *testing.T) {
	accountID := int64(202)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-existing",
				"access_token":  "at-existing",
				"id_token":      "id-existing",
				"base_url":      "https://old.example.com",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	// 模拟前端编辑：仅修改 base_url，没有传 token（脱敏后前端 spread 拿不到敏感键）
	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"base_url": "https://new.example.com",
		},
	})

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)

	// 敏感键应保留
	require.Equal(t, "rt-existing", repo.account.Credentials["refresh_token"])
	require.Equal(t, "at-existing", repo.account.Credentials["access_token"])
	require.Equal(t, "id-existing", repo.account.Credentials["id_token"])
	// 非敏感键被替换
	require.Equal(t, "https://new.example.com", repo.account.Credentials["base_url"])
}

func TestUpdateAccount_ExplicitNewTokenOverwrites(t *testing.T) {
	accountID := int64(203)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-old",
				"api_key":       "sk-old",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	updated, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{
			"refresh_token": "rt-new",
			// api_key 没传 → 应保留旧值
		},
	})
	require.NoError(t, err)
	require.NotNil(t, updated)

	require.Equal(t, "rt-new", repo.account.Credentials["refresh_token"])
	require.Equal(t, "sk-old", repo.account.Credentials["api_key"])
}

func TestUpdateAccount_EmptyCredentialsSkipsUpdate(t *testing.T) {
	accountID := int64(204)
	repo := &updateAccountCredsRepoStub{
		account: &Account{
			ID:       accountID,
			Platform: PlatformAnthropic,
			Type:     AccountTypeOAuth,
			Status:   StatusActive,
			Credentials: map[string]any{
				"refresh_token": "rt-existing",
			},
		},
	}
	svc := &adminServiceImpl{accountRepo: repo}

	_, err := svc.UpdateAccount(context.Background(), accountID, &UpdateAccountInput{
		Credentials: map[string]any{}, // len == 0 → 闸门跳过
		Name:        "renamed",
	})
	require.NoError(t, err)

	require.Equal(t, "rt-existing", repo.account.Credentials["refresh_token"], "空 credentials 不应触碰已有 token")
	require.Equal(t, "renamed", repo.account.Name)
}

func TestUpdateAccount_ResolvesNewAPIMoonshotRegionBeforePersist(t *testing.T) {
	fail := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "wrong region", http.StatusUnauthorized)
	}))
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	t.Cleanup(fail.Close)
	t.Cleanup(ok.Close)
	newapifusion.SetMoonshotProbeBasesForTest([]string{fail.URL, ok.URL})
	t.Cleanup(func() { newapifusion.SetMoonshotProbeBasesForTest(nil) })

	const accountID = int64(205)
	repo := &updateAccountCredsRepoStub{account: &Account{
		ID:          accountID,
		Name:        "moonshot",
		Platform:    PlatformNewAPI,
		Type:        AccountTypeAPIKey,
		ChannelType: newapiconstant.ChannelTypeMoonshot,
		Status:      StatusActive,
		Credentials: map[string]any{
			"base_url": "https://api.moonshot.cn",
			"api_key":  "sk-old",
		},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(
		context.Background(),
		accountID,
		&UpdateAccountInput{Credentials: map[string]any{"api_key": "sk-international"}},
	)

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, strings.TrimRight(ok.URL, "/"), repo.account.Credentials["base_url"])
}

func TestUpdateAccount_ValidatesExplicitGrokRefreshTokenBeforePersist(t *testing.T) {
	const accountID = int64(206)
	repo := &updateAccountCredsRepoStub{account: &Account{
		ID:       accountID,
		Name:     "grok-oauth",
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"refresh_token":  "old-refresh",
			"token_endpoint": "http://127.0.0.1/should-not-be-called",
		},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(
		context.Background(),
		accountID,
		&UpdateAccountInput{Credentials: map[string]any{
			"refresh_token":  "new-refresh",
			"token_endpoint": "http://127.0.0.1/should-not-be-called",
		}},
	)

	require.Nil(t, updated)
	require.ErrorContains(t, err, "grok refresh_token validation failed")
	require.ErrorContains(t, err, "invalid endpoint")
	require.Zero(t, repo.updateCalls, "invalid explicit refresh token must be rejected before persistence")
}

func TestUpdateAccount_GrokUnrelatedEditDoesNotRefreshToken(t *testing.T) {
	const accountID = int64(207)
	repo := &updateAccountCredsRepoStub{account: &Account{
		ID:       accountID,
		Name:     "grok-oauth",
		Platform: PlatformGrok,
		Type:     AccountTypeOAuth,
		Status:   StatusActive,
		Credentials: map[string]any{
			"refresh_token":  "existing-refresh",
			"token_endpoint": "http://127.0.0.1/should-not-be-called",
		},
	}}

	updated, err := (&adminServiceImpl{accountRepo: repo}).UpdateAccount(
		context.Background(),
		accountID,
		&UpdateAccountInput{Name: "renamed"},
	)

	require.NoError(t, err)
	require.NotNil(t, updated)
	require.Equal(t, 1, repo.updateCalls)
	require.Equal(t, "renamed", repo.account.Name)
}
