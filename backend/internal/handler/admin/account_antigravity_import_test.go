package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestParseAntigravityOAuthImportEntriesSupportsExportJSON(t *testing.T) {
	raw := `{
  "type": "antigravity",
  "access_token": "access-token-1",
  "refresh_token": "refresh-token-1",
  "expires_in": 3599,
  "timestamp": 1784599720635,
  "expired": "2099-07-21T03:08:39Z",
  "email": "user@example.com",
  "project_id": "crypto-cosine-qzp2g"
}`

	entries, err := parseAntigravityOAuthImportEntries(AntigravityOAuthImportRequest{Content: raw})
	require.NoError(t, err)
	require.Len(t, entries, 1)

	item, err := normalizeAntigravityImportObject(context.Background(), nil, AntigravityOAuthImportRequest{}, &antigravityImportAccount{
		Credentials: map[string]any{},
		Extra:       map[string]any{},
	}, entries[0].Value.(map[string]any), 1, time.Now().UTC())
	require.NoError(t, err)
	require.Equal(t, "access-token-1", item.Credentials["access_token"])
	require.Equal(t, "refresh-token-1", item.Credentials["refresh_token"])
	require.Equal(t, "user@example.com", item.Credentials["email"])
	require.Equal(t, "crypto-cosine-qzp2g", item.Credentials["project_id"])
	require.NotEmpty(t, item.Credentials["expires_at"])
}

func TestImportAntigravityOAuthCreatesAccountFromExportJSON(t *testing.T) {
	svc := newAntigravityImportMemoryAdminService(nil)
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	req := AntigravityOAuthImportRequest{
		Name:                 "ops-import",
		SkipDefaultGroupBind: boolPtr(true),
		FillProjectID:        boolPtr(false),
	}
	entries := []antigravityImportEntry{{
		Index: 1,
		Value: map[string]any{
			"type":          "antigravity",
			"access_token":  "access-token-1",
			"refresh_token": "refresh-token-1",
			"expired":       "2099-07-21T03:08:39Z",
			"email":         "user@example.com",
			"project_id":    "crypto-cosine-qzp2g",
		},
	}}

	result, err := handler.importAntigravityOAuthAccounts(context.Background(), req, entries)
	require.NoError(t, err)
	require.Equal(t, 1, result.Created)
	require.Equal(t, 0, result.Failed)
	require.Len(t, svc.createdAccounts, 1)
	got := svc.createdAccounts[0]
	require.Equal(t, service.PlatformAntigravity, got.Platform)
	require.Equal(t, service.AccountTypeOAuth, got.Type)
	require.Equal(t, "ops-import", got.Name)
	require.Equal(t, "crypto-cosine-qzp2g", got.Credentials["project_id"])
	require.Equal(t, "user@example.com", got.AccountEmail)
}

func TestImportAntigravityOAuthRejectsMissingAccessToken(t *testing.T) {
	svc := newAntigravityImportMemoryAdminService(nil)
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	req := AntigravityOAuthImportRequest{SkipDefaultGroupBind: boolPtr(true), FillProjectID: boolPtr(false)}
	entries := []antigravityImportEntry{{
		Index: 1,
		Value: map[string]any{
			"email":      "user@example.com",
			"project_id": "crypto-cosine-qzp2g",
		},
	}}

	result, err := handler.importAntigravityOAuthAccounts(context.Background(), req, entries)
	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	require.Empty(t, svc.createdAccounts)
}

func TestImportAntigravityOAuthUpdatesExistingByRefreshToken(t *testing.T) {
	existing := service.Account{
		ID:       10,
		Name:     "existing",
		Platform: service.PlatformAntigravity,
		Type:     service.AccountTypeOAuth,
		Credentials: map[string]any{
			"refresh_token": "refresh-token-1",
			"access_token":  "old-access",
			"project_id":    "crypto-cosine-qzp2g",
			"email":         "user@example.com",
		},
	}
	svc := newAntigravityImportMemoryAdminService([]service.Account{existing})
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	req := AntigravityOAuthImportRequest{SkipDefaultGroupBind: boolPtr(true), FillProjectID: boolPtr(false)}
	entries := []antigravityImportEntry{{
		Index: 1,
		Value: map[string]any{
			"access_token":  "new-access",
			"refresh_token": "refresh-token-1",
			"expired":       "2099-07-21T03:08:39Z",
			"email":         "user@example.com",
			"project_id":    "crypto-cosine-qzp2g",
		},
	}}

	result, err := handler.importAntigravityOAuthAccounts(context.Background(), req, entries)
	require.NoError(t, err)
	require.Equal(t, 1, result.Updated)
	require.Empty(t, svc.createdAccounts)
	require.Len(t, svc.updatedAccounts, 1)
	require.Equal(t, "new-access", svc.updatedAccounts[0].input.Credentials["access_token"])
}

func TestImportAntigravityOAuthRejectsMissingProjectIDWhenFillDisabled(t *testing.T) {
	svc := newAntigravityImportMemoryAdminService(nil)
	handler := NewAccountHandler(svc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	req := AntigravityOAuthImportRequest{
		SkipDefaultGroupBind: boolPtr(true),
		FillProjectID:        boolPtr(false),
	}
	entries := []antigravityImportEntry{{
		Index: 1,
		Value: map[string]any{
			"access_token":  "access-token-1",
			"refresh_token": "refresh-token-1",
			"expired":       "2099-07-21T03:08:39Z",
			"email":         "user@example.com",
		},
	}}

	result, err := handler.importAntigravityOAuthAccounts(context.Background(), req, entries)
	require.NoError(t, err)
	require.Equal(t, 1, result.Failed)
	require.Empty(t, svc.createdAccounts)
}

func TestImportAntigravityOAuthEndpointReturnsBatchResult(t *testing.T) {
	svc := newAntigravityImportMemoryAdminService(nil)
	router := setupAntigravityImportRouter(svc)
	body, err := json.Marshal(map[string]any{
		"name": "ops-import",
		"content": `{
  "access_token": "access-token-1",
  "refresh_token": "refresh-token-1",
  "expired": "2099-07-21T03:08:39Z",
  "email": "user@example.com",
  "project_id": "crypto-cosine-qzp2g"
}`,
		"skip_default_group_bind": true,
		"fill_project_id":         false,
	})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/accounts/import/antigravity-oauth", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Len(t, svc.createdAccounts, 1)
}

type antigravityImportMemoryAdminService struct {
	*stubAdminService
	nextID          int64
	updatedAccounts []struct {
		id    int64
		input *service.UpdateAccountInput
	}
}

func newAntigravityImportMemoryAdminService(accounts []service.Account) *antigravityImportMemoryAdminService {
	stub := newStubAdminService()
	stub.accounts = append([]service.Account(nil), accounts...)
	return &antigravityImportMemoryAdminService{
		stubAdminService: stub,
		nextID:           100,
	}
}

func (s *antigravityImportMemoryAdminService) CreateAccount(ctx context.Context, input *service.CreateAccountInput) (*service.Account, error) {
	s.createdAccounts = append(s.createdAccounts, input)
	if s.createAccountErr != nil {
		return nil, s.createAccountErr
	}
	account := service.Account{
		ID:          s.nextID,
		Name:        input.Name,
		Platform:    input.Platform,
		Type:        input.Type,
		Status:      service.StatusActive,
		Credentials: cloneAntigravityImportMap(input.Credentials),
		Extra:       cloneAntigravityImportMap(input.Extra),
	}
	s.nextID++
	s.accounts = append(s.accounts, account)
	return &account, nil
}

func (s *antigravityImportMemoryAdminService) UpdateAccount(ctx context.Context, id int64, input *service.UpdateAccountInput) (*service.Account, error) {
	s.updatedAccounts = append(s.updatedAccounts, struct {
		id    int64
		input *service.UpdateAccountInput
	}{id: id, input: input})
	if s.updateAccountErr != nil {
		return nil, s.updateAccountErr
	}
	for i := range s.accounts {
		if s.accounts[i].ID != id {
			continue
		}
		account := s.accounts[i]
		if input.Credentials != nil {
			account.Credentials = cloneAntigravityImportMap(input.Credentials)
		}
		if input.Extra != nil {
			account.Extra = cloneAntigravityImportMap(input.Extra)
		}
		s.accounts[i] = account
		return &account, nil
	}
	return nil, service.ErrAccountNotFound
}

func setupAntigravityImportRouter(adminSvc *antigravityImportMemoryAdminService) *gin.Engine {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	handler := NewAccountHandler(adminSvc, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	router.POST("/api/v1/admin/accounts/import/antigravity-oauth", handler.ImportAntigravityOAuth)
	return router
}
