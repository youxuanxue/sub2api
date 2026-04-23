//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// US-029 — Bug B-4 verification.
//
// BulkUpdateAccounts persists credentials directly via accountRepo.BulkUpdate,
// which bypasses resolveNewAPIMoonshotBaseURLOnSave (the per-save Moonshot
// regional probe wired into CreateAccount / UpdateAccount). A batch api_key
// swap on Moonshot accounts would persist with a wrong base_url
// (api.moonshot.cn vs .ai), and the relay hot path 401s every request because
// it deliberately does NOT do per-request region fallback.
//
// Fix: when any account in the batch is on PlatformNewAPI AND credentials are
// being edited, reject the request. Operator must edit per-account.
//
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-4.

// us029StubAdminService extends stubAdminService so we can return per-account
// platform values from GetAccountsByIDs (the stub default returns blank
// Platform). We also short-circuit BulkUpdateAccounts to confirm whether the
// reject guard kicked in BEFORE service was called.
type us029StubAdminService struct {
	*stubAdminService
	platformByID         map[int64]string
	bulkUpdateInvocations int
}

func (s *us029StubAdminService) GetAccountsByIDs(ctx context.Context, ids []int64) ([]*service.Account, error) {
	out := make([]*service.Account, 0, len(ids))
	for _, id := range ids {
		acc := &service.Account{
			ID:       id,
			Name:     "account",
			Status:   service.StatusActive,
			Platform: s.platformByID[id],
		}
		out = append(out, acc)
	}
	return out, nil
}

func (s *us029StubAdminService) BulkUpdateAccounts(ctx context.Context, input *service.BulkUpdateAccountsInput) (*service.BulkUpdateAccountsResult, error) {
	s.bulkUpdateInvocations++
	return &service.BulkUpdateAccountsResult{
		Success:    len(input.AccountIDs),
		SuccessIDs: input.AccountIDs,
	}, nil
}

func us029NewHandler(stub *us029StubAdminService) *AccountHandler {
	return &AccountHandler{adminService: stub}
}

func us029PostBulkUpdate(t *testing.T, h *AccountHandler, payload BulkUpdateAccountsRequest) (int, map[string]any) {
	t.Helper()
	body, err := json.Marshal(payload)
	require.NoError(t, err)

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodPost, "/api/v1/admin/accounts/bulk-update", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")

	h.BulkUpdate(c)

	respBody := map[string]any{}
	if w.Body.Len() > 0 {
		_ = json.Unmarshal(w.Body.Bytes(), &respBody)
	}
	return w.Code, respBody
}

func TestUS029_BulkUpdate_NewAPICredentials_Rejected(t *testing.T) {
	stub := &us029StubAdminService{
		stubAdminService: newStubAdminService(),
		platformByID: map[int64]string{
			101: service.PlatformNewAPI,
			102: service.PlatformOpenAI,
		},
	}
	h := us029NewHandler(stub)

	code, body := us029PostBulkUpdate(t, h, BulkUpdateAccountsRequest{
		AccountIDs:  []int64{101, 102},
		Credentials: map[string]any{"api_key": "sk-new"},
	})

	require.Equal(t, http.StatusBadRequest, code, "newapi credentials bulk edit must be rejected")
	require.Equal(t, 0, stub.bulkUpdateInvocations, "BulkUpdateAccounts must NOT be called")
	// Error envelope shape: top-level error code or message containing reject hint.
	flat := flatten(body)
	require.Contains(t, flat, "BULK_CREDENTIALS_UNSUPPORTED_FOR_NEWAPI",
		"error message must surface the reject reason; got %s", flat)
}

func TestUS029_BulkUpdate_NewAPI_NonCredentialFields_Allowed(t *testing.T) {
	stub := &us029StubAdminService{
		stubAdminService: newStubAdminService(),
		platformByID: map[int64]string{
			201: service.PlatformNewAPI,
			202: service.PlatformOpenAI,
		},
	}
	h := us029NewHandler(stub)

	priority := 5
	code, _ := us029PostBulkUpdate(t, h, BulkUpdateAccountsRequest{
		AccountIDs: []int64{201, 202},
		Priority:   &priority,
	})

	require.Equal(t, http.StatusOK, code, "non-credentials bulk edit must succeed even on newapi")
	require.Equal(t, 1, stub.bulkUpdateInvocations, "BulkUpdateAccounts must run normally when credentials absent")
}

func TestUS029_BulkUpdate_OpenAICredentials_Allowed(t *testing.T) {
	// Regression: only PlatformNewAPI is rejected; other platforms keep
	// existing bulk credentials behavior.
	stub := &us029StubAdminService{
		stubAdminService: newStubAdminService(),
		platformByID: map[int64]string{
			301: service.PlatformOpenAI,
			302: service.PlatformAnthropic,
		},
	}
	h := us029NewHandler(stub)

	code, _ := us029PostBulkUpdate(t, h, BulkUpdateAccountsRequest{
		AccountIDs:  []int64{301, 302},
		Credentials: map[string]any{"api_key": "sk-new"},
	})

	require.Equal(t, http.StatusOK, code, "openai/anthropic bulk credentials must still work")
	require.Equal(t, 1, stub.bulkUpdateInvocations)
}

func TestUS029_BulkUpdate_EmptyCredentials_NotGuardChecked(t *testing.T) {
	// Defense-in-depth: passing an empty credentials map must not trigger
	// the guard (no Moonshot exposure when nothing's actually being changed).
	stub := &us029StubAdminService{
		stubAdminService: newStubAdminService(),
		platformByID:     map[int64]string{401: service.PlatformNewAPI},
	}
	h := us029NewHandler(stub)
	priority := 5

	code, _ := us029PostBulkUpdate(t, h, BulkUpdateAccountsRequest{
		AccountIDs:  []int64{401},
		Priority:    &priority,
		Credentials: map[string]any{}, // empty -> len()==0 -> guard skipped
	})

	require.Equal(t, http.StatusOK, code, "empty credentials map must not trigger reject")
	require.Equal(t, 1, stub.bulkUpdateInvocations)
}

// flatten returns a JSON-serialised single-line representation of body for
// substring assertions (avoids brittle structural matching across error
// envelope shapes).
func flatten(body map[string]any) string {
	bs, _ := json.Marshal(body)
	return string(bs)
}
