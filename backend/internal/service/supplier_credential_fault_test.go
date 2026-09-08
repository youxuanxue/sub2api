//go:build unit

package service

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

type supplierFaultRepo struct {
	rateLimitAccountRepoStub
	accounts []Account
	listErr  error
}

func (r *supplierFaultRepo) ListSupplierCredentialFaultAccounts(context.Context) ([]Account, error) {
	return append([]Account(nil), r.accounts...), r.listErr
}

func (r *supplierFaultRepo) GetByID(_ context.Context, id int64) (*Account, error) {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			return cloneSupplierProjectionAccount(&r.accounts[i]), nil
		}
	}
	return nil, ErrAccountNotFound
}

func (r *supplierFaultRepo) ClearError(_ context.Context, id int64) error {
	for i := range r.accounts {
		if r.accounts[i].ID == id {
			r.accounts[i].Status, r.accounts[i].ErrorMessage = StatusActive, ""
			r.accounts[i].Schedulable = true
			return nil
		}
	}
	return ErrAccountNotFound
}

func (r *supplierFaultRepo) UpdateSupplierCredentialFault(_ context.Context, snapshot *Account, message string) (bool, error) {
	for i := range r.accounts {
		account := &r.accounts[i]
		if account.ID != snapshot.ID || !reflect.DeepEqual(account.Credentials, snapshot.Credentials) ||
			account.Status != snapshot.Status || account.ErrorMessage != snapshot.ErrorMessage {
			continue
		}
		account.Status, account.ErrorMessage = StatusError, message
		if message == "" {
			account.Status = StatusActive
		}
		return true, nil
	}
	return false, nil
}

func newSupplierFaultAccount(id, sourceID int64, channelType int, key, upstreamModel string) Account {
	return Account{
		ID: id, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: channelType,
		Status: StatusActive, Schedulable: true, Concurrency: 1000,
		Credentials: supplierManagedCredentials("https://supplier.example/v1", key, map[string]string{"claude-opus-4-6": upstreamModel}, channelType),
		Extra:       map[string]any{SupplierSourceIDExtraKey: sourceID, SupplierDiscountBandExtraKey: 2},
	}
}

func newSupplierFaultService(accounts ...Account) (*RateLimitService, *supplierFaultRepo, *runtimeBlockRecorder) {
	repo := &supplierFaultRepo{accounts: accounts}
	cfg := &config.Config{}
	cfg.Totp.EncryptionKey = "supplier-fault-test-encryption-key"
	svc := NewRateLimitService(repo, nil, cfg, nil, nil)
	blocker := &runtimeBlockRecorder{}
	svc.SetAccountRuntimeBlocker(blocker)
	return svc, repo, blocker
}

func TestUS050_SupplierCredentialFaultSharesAcrossProtocolProjections(t *testing.T) {
	for _, pair := range []struct {
		name          string
		first, second Account
	}{
		{"same_model_115_124", newSupplierFaultAccount(115, 10, 1, "shared", "claude-fable-5"), newSupplierFaultAccount(124, 11, 14, "shared", "claude-fable-5")},
		{"different_upstream_models_123_126", newSupplierFaultAccount(123, 12, 14, "shared", "claude-opus-4-6"), newSupplierFaultAccount(126, 13, 1, "shared", "claude-opus-4-6-thinking")},
	} {
		t.Run(pair.name, func(t *testing.T) {
			pair.second.Credentials["base_url"] = "https://SUPPLIER.example/v1/"
			otherKey := newSupplierFaultAccount(201, 14, 1, "other-key", "claude-opus-4-6")
			otherEndpoint := newSupplierFaultAccount(202, 15, 14, "shared", "claude-opus-4-6")
			otherEndpoint.Credentials["base_url"] = "https://another.example/v1"
			unmanaged := newSupplierFaultAccount(203, 16, 1, "shared", "claude-opus-4-6")
			unmanaged.Extra = nil
			svc, repo, blocker := newSupplierFaultService(pair.first, pair.second, otherKey, otherEndpoint, unmanaged)
			// Scheduler snapshots retain exclusive protocol credentials but omit supplier metadata.
			origin := pair.first
			origin.Extra = nil
			disabled := svc.HandleUpstreamError(context.Background(), &origin, http.StatusUnauthorized, nil,
				[]byte(`{"error":{"code":"invalid_api_key","message":"Invalid API key"}}`))
			require.True(t, disabled)
			require.Len(t, blocker.accounts, 2)
			for i, expected := range []Account{pair.first, pair.second} {
				require.Equal(t, StatusError, repo.accounts[i].Status)
				require.True(t, IsSupplierCredentialFault(repo.accounts[i].ErrorMessage))
				require.Equal(t, expected.Credentials, repo.accounts[i].Credentials)
				require.Equal(t, expected.ChannelType, repo.accounts[i].ChannelType)
				require.Equal(t, expected.Concurrency, repo.accounts[i].Concurrency)
			}
			for _, account := range repo.accounts[2:] {
				require.Equal(t, StatusActive, account.Status)
			}
		})
	}
}

func TestUS050_SupplierCredentialRecoveryPreservesPeerModelLimitsAndManualPause(t *testing.T) {
	first := newSupplierFaultAccount(123, 12, 14, "shared", "claude-opus-4-6")
	second := newSupplierFaultAccount(126, 13, 1, "shared", "claude-opus-4-6-thinking")
	second.Schedulable = false
	modelLimit := map[string]any{"claude-opus-4-6-thinking": map[string]any{"rate_limit_reset_at": "2099-01-01T00:00:00Z"}}
	second.Extra["model_rate_limits"] = modelLimit
	unrelatedFault := newSupplierFaultAccount(127, 14, 14, "shared", "claude-opus-4-6")
	unrelatedFault.Status, unrelatedFault.ErrorMessage = StatusError, "Protocol configuration invalid"
	svc, repo, blocker := newSupplierFaultService(first, second, unrelatedFault)

	require.True(t, svc.HandleUpstreamError(context.Background(), &first, http.StatusPaymentRequired, nil,
		[]byte(`{"error":{"message":"Insufficient Balance"}}`)))
	require.Equal(t, unrelatedFault.ErrorMessage, repo.accounts[2].ErrorMessage)
	result, err := svc.RecoverAccountAfterSuccessfulTest(context.Background(), first.ID)
	require.NoError(t, err)
	require.True(t, result.ClearedError)
	require.Equal(t, StatusActive, repo.accounts[0].Status)
	require.Equal(t, StatusActive, repo.accounts[1].Status)
	require.False(t, repo.accounts[1].Schedulable, "shared recovery must preserve the peer's manual pause")
	require.Equal(t, modelLimit, repo.accounts[1].Extra["model_rate_limits"])
	require.Equal(t, unrelatedFault.ErrorMessage, repo.accounts[2].ErrorMessage)
	require.ElementsMatch(t, []int64{123, 126}, blocker.clearedIDs)
}

func TestUS050_SupplierCredentialFaultDoesNotWidenAmbiguousErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"generic_401", 401, `{"error":{"message":"Unauthorized"}}`},
		{"html_401", 401, `<html>Invalid API key</html>`},
		{"model_401", 401, `{"error":{"code":"invalid_model","message":"The model does not exist or you do not have access to it."}}`},
		{"protocol_403", 403, `{"error":{"message":"Endpoint not supported"}}`},
		{"rpm_429", 429, `{"error":{"message":"Rate limit exceeded"}}`},
		{"timeout", 504, `{"error":{"message":"Timeout"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			first := newSupplierFaultAccount(115, 10, 1, "shared", "claude-fable-5")
			second := newSupplierFaultAccount(124, 11, 14, "shared", "claude-fable-5")
			svc, repo, _ := newSupplierFaultService(first, second)
			svc.HandleUpstreamError(context.Background(), &first, tc.status, nil, []byte(tc.body))
			require.Equal(t, StatusActive, repo.accounts[1].Status)
			require.Empty(t, repo.accounts[1].ErrorMessage)
		})
	}
}

func TestUS050_SupplierCredentialRecoveryReportsQueryFailure(t *testing.T) {
	account := newSupplierFaultAccount(115, 10, 1, "shared", "claude-fable-5")
	account.Status, account.ErrorMessage = StatusError, supplierCredentialFaultPrefix+"Invalid API key"
	svc, repo, _ := newSupplierFaultService(account)
	repo.listErr = errors.New("database unavailable")
	_, err := svc.RecoverAccountAfterSuccessfulTest(context.Background(), account.ID)
	require.ErrorContains(t, err, "database unavailable")
	require.Equal(t, StatusError, repo.accounts[0].Status)
}

func TestUS050_AdminClearErrorRecoversSupplierCredentialPeers(t *testing.T) {
	first := newSupplierFaultAccount(115, 10, 1, "shared", "claude-fable-5")
	second := newSupplierFaultAccount(124, 11, 14, "shared", "claude-fable-5")
	first.Status, first.ErrorMessage = StatusError, supplierCredentialFaultPrefix+"Invalid API key"
	second.Status, second.ErrorMessage = first.Status, first.ErrorMessage
	second.Schedulable = false
	svc, repo, blocker := newSupplierFaultService(first, second)
	admin := &adminServiceImpl{accountRepo: repo, runtimeBlocker: blocker, rateLimitService: svc}
	updated, err := admin.ClearAccountError(context.Background(), first.ID)
	require.NoError(t, err)
	require.Equal(t, StatusActive, updated.Status)
	require.Equal(t, StatusActive, repo.accounts[1].Status)
	require.False(t, repo.accounts[1].Schedulable)
	require.ElementsMatch(t, []int64{115, 124}, blocker.clearedIDs)
}

func TestUS050_SupplierCredentialFailureFromRotatedCredentialDoesNotBlockPeers(t *testing.T) {
	for _, peerKey := range []string{"old-key", "new-key"} {
		t.Run(peerKey, func(t *testing.T) {
			staleOrigin := newSupplierFaultAccount(115, 10, 1, "old-key", "claude-fable-5")
			rotatedOrigin := newSupplierFaultAccount(115, 10, 1, "new-key", "claude-fable-5")
			peer := newSupplierFaultAccount(124, 11, 14, peerKey, "claude-fable-5")
			svc, repo, blocker := newSupplierFaultService(rotatedOrigin, peer)
			require.True(t, svc.HandleUpstreamError(context.Background(), &staleOrigin, http.StatusUnauthorized, nil,
				[]byte(`{"error":{"code":"invalid_api_key","message":"Invalid API key"}}`)))
			for _, account := range repo.accounts {
				require.Equal(t, StatusActive, account.Status)
			}
			require.Empty(t, blocker.accounts)
			require.Zero(t, repo.setErrorCalls, "a stale failure must not fall back to an unconditional account write")
		})
	}
}
