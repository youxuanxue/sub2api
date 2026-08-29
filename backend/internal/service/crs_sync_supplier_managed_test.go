package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestUS048_CRSSyncRejectsSupplierManagedAccountOverwrite(t *testing.T) {
	repo := &crsSupplierManagedAccountRepo{stored: &Account{
		ID: 41, Name: "佳杰/stbl-5 · 档位 3", Platform: PlatformNewAPI, Type: AccountTypeAPIKey,
		Credentials: map[string]any{"base_url": "https://supplier.example/v1", "api_key": "supplier-secret"},
		Extra: map[string]any{
			"crs_account_id":             "crs-1",
			SupplierSourceIDExtraKey:     int64(7),
			SupplierDiscountBandExtraKey: 3,
		},
		Priority: 103, Status: StatusActive, Schedulable: true,
	}}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/web/auth/login" {
			_, _ = response.Write([]byte(`{"success":true,"token":"admin-token"}`))
			return
		}
		require.Equal(t, "/admin/sync/export-accounts", request.URL.Path)
		require.NoError(t, json.NewEncoder(response).Encode(map[string]any{
			"success": true,
			"data": map[string]any{"claudeConsoleAccounts": []any{map[string]any{
				"kind": "claude-console", "id": "crs-1", "name": "CRS overwrite",
				"isActive": true, "schedulable": true, "priority": 1,
				"credentials": map[string]any{"api_key": "crs-secret"},
			}}},
		}))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc := NewCRSSyncService(repo, nil, nil, nil, nil, cfg)

	result, err := svc.SyncFromCRS(context.Background(), SyncFromCRSInput{
		BaseURL: server.URL, Username: "admin", Password: "password",
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	require.Zero(t, result.Updated)
	require.Len(t, result.Items, 1)
	require.Equal(t, "failed", result.Items[0].Action)
	require.Contains(t, result.Items[0].Error, "supplier-managed")
	require.Zero(t, repo.updateCalls)
	require.Equal(t, "佳杰/stbl-5 · 档位 3", repo.stored.Name)
	require.Equal(t, "supplier-secret", repo.stored.Credentials["api_key"])
}

func TestUS048_CRSSyncRejectsReservedSupplierExtraOnNewAccount(t *testing.T) {
	repo := &crsSupplierManagedAccountRepo{}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/web/auth/login" {
			_, _ = response.Write([]byte(`{"success":true,"token":"admin-token"}`))
			return
		}
		require.Equal(t, "/admin/sync/export-accounts", request.URL.Path)
		require.NoError(t, json.NewEncoder(response).Encode(map[string]any{
			"success": true,
			"data": map[string]any{"openaiResponsesAccounts": []any{map[string]any{
				"kind": "openai-responses", "id": "crs-new", "name": "forged supplier account",
				"isActive": true, "schedulable": true, "priority": 100,
				"credentials": map[string]any{"api_key": "secret", "base_url": "https://example.com/v1"},
				"extra": map[string]any{
					SupplierSourceIDExtraKey: int64(7), SupplierDiscountBandExtraKey: 3,
				},
			}}},
		}))
	}))
	t.Cleanup(server.Close)

	cfg := &config.Config{}
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc := NewCRSSyncService(repo, nil, nil, nil, nil, cfg)

	result, err := svc.SyncFromCRS(context.Background(), SyncFromCRSInput{
		BaseURL: server.URL, Username: "admin", Password: "password",
	})

	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	require.Zero(t, result.Created)
	require.Len(t, result.Items, 1)
	require.Equal(t, "failed", result.Items[0].Action)
	require.Contains(t, result.Items[0].Error, "reserved")
	require.Zero(t, repo.createCalls)
}

type crsSupplierManagedAccountRepo struct {
	AccountRepository
	stored      *Account
	createCalls int
	updateCalls int
}

func (r *crsSupplierManagedAccountRepo) GetByCRSAccountID(context.Context, string) (*Account, error) {
	return cloneSupplierProjectionAccount(r.stored), nil
}

func (r *crsSupplierManagedAccountRepo) ListShadowsByParent(context.Context, int64) ([]*Account, error) {
	return nil, nil
}

func (r *crsSupplierManagedAccountRepo) Update(_ context.Context, account *Account) error {
	r.updateCalls++
	r.stored = cloneSupplierProjectionAccount(account)
	return nil
}

func (r *crsSupplierManagedAccountRepo) Create(_ context.Context, account *Account) error {
	r.createCalls++
	r.stored = cloneSupplierProjectionAccount(account)
	return nil
}
