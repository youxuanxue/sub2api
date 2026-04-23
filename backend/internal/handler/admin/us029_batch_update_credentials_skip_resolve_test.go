//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// US-029 — Bug B-5 verification.
//
// BatchUpdateCredentials previously routed through UpdateAccount, which
// re-runs Moonshot regional probes (resolveNewAPIMoonshotBaseURLOnSave),
// quota-reset recompute, group-binding validation, OAuth-privacy goroutine
// spawn, etc. on every single account in the batch. The field whitelist
// (account_uuid|org_uuid|intercept_warmup_requests) has zero overlap with
// credentials.api_key / base_url, so all of that work is wasted — and on
// newapi/Moonshot accounts the per-call probe alone can take 25s to time
// out, turning a UUID rename into a multi-minute fan-out.
//
// Fix: route through AdminService.UpdateAccountCredentials, the new
// credentials-only writer that bypasses UpdateAccount's side effects.
//
// See docs/bugs/2026-04-22-newapi-and-bridge-deep-audit.md § B-5.

// us029BatchCredentialsRecorder records which AdminService methods were
// invoked so we can assert that UpdateAccount is NOT called and
// UpdateAccountCredentials IS.
type us029BatchCredentialsRecorder struct {
	*stubAdminService
	updateAccountCalls            atomic.Int64
	updateAccountCredentialsCalls atomic.Int64
	lastCredentials               map[int64]map[string]any
}

func (r *us029BatchCredentialsRecorder) UpdateAccount(ctx context.Context, id int64, input *service.UpdateAccountInput) (*service.Account, error) {
	r.updateAccountCalls.Add(1)
	return r.stubAdminService.UpdateAccount(ctx, id, input)
}

func (r *us029BatchCredentialsRecorder) UpdateAccountCredentials(ctx context.Context, id int64, credentials map[string]any) error {
	r.updateAccountCredentialsCalls.Add(1)
	if r.lastCredentials == nil {
		r.lastCredentials = map[int64]map[string]any{}
	}
	cloned := make(map[string]any, len(credentials))
	for k, v := range credentials {
		cloned[k] = v
	}
	r.lastCredentials[id] = cloned
	return nil
}

func TestUS029_BatchUpdateCredentials_AccountUUID_DoesNotTriggerUpdateAccount(t *testing.T) {
	rec := &us029BatchCredentialsRecorder{stubAdminService: newStubAdminService()}
	router, _ := setupAccountHandlerWithService(rec)

	body, _ := json.Marshal(BatchUpdateCredentialsRequest{
		AccountIDs: []int64{1, 2, 3},
		Field:      "account_uuid",
		Value:      "uuid-x",
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/accounts/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(0), rec.updateAccountCalls.Load(),
		"BatchUpdateCredentials must NOT call UpdateAccount (would trigger Moonshot probes)")
	require.Equal(t, int64(3), rec.updateAccountCredentialsCalls.Load(),
		"BatchUpdateCredentials must call UpdateAccountCredentials per account")
}

func TestUS029_BatchUpdateCredentials_PreservesExistingCredentialFields(t *testing.T) {
	// The handler reads each account's existing credentials, sets the target
	// field, and passes the whole map to UpdateAccountCredentials. Verify
	// the merged map (not just the new field) reaches the writer.
	rec := &us029BatchCredentialsRecorder{stubAdminService: newStubAdminService()}
	router, _ := setupAccountHandlerWithService(rec)

	body, _ := json.Marshal(BatchUpdateCredentialsRequest{
		AccountIDs: []int64{42},
		Field:      "org_uuid",
		Value:      "org-1",
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/accounts/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, rec.lastCredentials[42])
	require.Equal(t, "org-1", rec.lastCredentials[42]["org_uuid"])
}

func TestUS029_BatchUpdateCredentials_InterceptWarmupRequests_RoutesThroughCredentialsWriter(t *testing.T) {
	rec := &us029BatchCredentialsRecorder{stubAdminService: newStubAdminService()}
	router, _ := setupAccountHandlerWithService(rec)

	body, _ := json.Marshal(map[string]any{
		"account_ids": []int64{7},
		"field":       "intercept_warmup_requests",
		"value":       true,
	})

	w := httptest.NewRecorder()
	req, _ := http.NewRequest(http.MethodPost, "/api/v1/admin/accounts/batch-update-credentials", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(0), rec.updateAccountCalls.Load(),
		"intercept_warmup_requests path must also bypass UpdateAccount")
	require.Equal(t, int64(1), rec.updateAccountCredentialsCalls.Load())
	require.Equal(t, true, rec.lastCredentials[7]["intercept_warmup_requests"])
}
