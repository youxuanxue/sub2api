//go:build unit

package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// --- stubs for the edge ops handler's narrow dependencies ---

type opsRateLimiterStub struct {
	clearedRateLimit  []int64
	clearedTempUnsched []int64
	err               error
}

func (s *opsRateLimiterStub) ClearRateLimit(_ context.Context, id int64) error {
	s.clearedRateLimit = append(s.clearedRateLimit, id)
	return s.err
}
func (s *opsRateLimiterStub) ClearTempUnschedulable(_ context.Context, id int64) error {
	s.clearedTempUnsched = append(s.clearedTempUnsched, id)
	return s.err
}

type opsAdminStub struct {
	account        *service.Account
	resetQuotaIDs  []int64
	schedulableSet map[int64]bool
	err            error
}

func (s *opsAdminStub) ResetAccountQuota(_ context.Context, id int64) error {
	s.resetQuotaIDs = append(s.resetQuotaIDs, id)
	return s.err
}
func (s *opsAdminStub) SetAccountSchedulable(_ context.Context, id int64, schedulable bool) (*service.Account, error) {
	if s.schedulableSet == nil {
		s.schedulableSet = map[int64]bool{}
	}
	s.schedulableSet[id] = schedulable
	if s.err != nil {
		return nil, s.err
	}
	cp := *s.account
	cp.Schedulable = schedulable
	return &cp, nil
}
func (s *opsAdminStub) GetAccount(_ context.Context, _ int64) (*service.Account, error) {
	return s.account, s.err
}

type opsUsageStub struct {
	usage       *service.UsageInfo
	activeCalls int
	err         error
}

func (s *opsUsageStub) GetUsage(_ context.Context, _ int64, _ ...bool) (*service.UsageInfo, error) {
	s.activeCalls++
	return s.usage, s.err
}
func (s *opsUsageStub) GetPassiveUsage(_ context.Context, _ int64) (*service.UsageInfo, error) {
	return s.usage, s.err
}

// edgeOpsAccount is a stub account carrying a SECRET in credentials so the test can
// assert the credential-free DTO never leaks it.
func edgeOpsAccount() *service.Account {
	return &service.Account{
		ID:          51,
		Name:        "edge-acct",
		Platform:    service.PlatformAnthropic,
		Type:        service.AccountTypeOAuth,
		Status:      service.StatusActive,
		Schedulable: true,
		Credentials: map[string]any{"api_key": "sk-SECRET-edge-key", "access_token": "tok-SECRET"},
	}
}

func newOpsHandler(rl *opsRateLimiterStub, ad *opsAdminStub, us *opsUsageStub) *EdgeAccountOpsHandler {
	return NewEdgeAccountOpsHandler(rl, ad, us)
}

func opsCtx(method, id string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/x", nil)
	c.Params = gin.Params{{Key: "id", Value: id}}
	return c, w
}

func TestEdgeAccountOps_ClearRateLimit_CallsServiceAndReturnsSanitizedDTO(t *testing.T) {
	rl := &opsRateLimiterStub{}
	ad := &opsAdminStub{account: edgeOpsAccount()}
	h := newOpsHandler(rl, ad, &opsUsageStub{})

	c, w := opsCtx(http.MethodPost, "51")
	h.ClearRateLimit(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []int64{51}, rl.clearedRateLimit)
	body := w.Body.String()
	require.Contains(t, body, "edge-acct")
	// CRITICAL: the credential-free DTO must never carry secrets through prod.
	require.NotContains(t, body, "sk-SECRET-edge-key")
	require.NotContains(t, body, "tok-SECRET")
	require.NotContains(t, body, "credentials")
}

func TestEdgeAccountOps_ResetQuota_CallsService(t *testing.T) {
	ad := &opsAdminStub{account: edgeOpsAccount()}
	h := newOpsHandler(&opsRateLimiterStub{}, ad, &opsUsageStub{})
	c, w := opsCtx(http.MethodPost, "51")
	h.ResetQuota(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []int64{51}, ad.resetQuotaIDs)
}

func TestEdgeAccountOps_ClearTempUnschedulable_CallsService(t *testing.T) {
	rl := &opsRateLimiterStub{}
	h := newOpsHandler(rl, &opsAdminStub{account: edgeOpsAccount()}, &opsUsageStub{})
	c, w := opsCtx(http.MethodDelete, "51")
	h.ClearTempUnschedulable(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []int64{51}, rl.clearedTempUnsched)
}

func TestEdgeAccountOps_SetSchedulable_BindsBodyAndReturnsUpdated(t *testing.T) {
	ad := &opsAdminStub{account: edgeOpsAccount()}
	h := newOpsHandler(&opsRateLimiterStub{}, ad, &opsUsageStub{})
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/x", strings.NewReader(`{"schedulable":false}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = gin.Params{{Key: "id", Value: "51"}}
	h.SetSchedulable(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, false, ad.schedulableSet[51])
}

func TestEdgeAccountOps_GetActiveUsage_CallsActiveByDefault(t *testing.T) {
	us := &opsUsageStub{usage: &service.UsageInfo{Source: "active"}}
	h := newOpsHandler(&opsRateLimiterStub{}, &opsAdminStub{account: edgeOpsAccount()}, us)
	c, w := opsCtx(http.MethodGet, "51")
	h.GetActiveUsage(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 1, us.activeCalls)
}

func TestEdgeAccountOps_BadIdIsBadRequest(t *testing.T) {
	h := newOpsHandler(&opsRateLimiterStub{}, &opsAdminStub{account: edgeOpsAccount()}, &opsUsageStub{})
	c, w := opsCtx(http.MethodPost, "not-a-number")
	h.ClearRateLimit(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEdgeAccountOps_ServiceErrorIsInternal(t *testing.T) {
	rl := &opsRateLimiterStub{err: context.DeadlineExceeded}
	h := newOpsHandler(rl, &opsAdminStub{account: edgeOpsAccount()}, &opsUsageStub{})
	c, w := opsCtx(http.MethodPost, "51")
	h.ClearRateLimit(c)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
