//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestAccountTestService_Kiro402ReachedLimitTriggersIncidentAlert(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/test", nil)

	upstream := &kiroStatusUpstream{
		status: http.StatusPaymentRequired,
		body:   "You have reached the limit.",
	}
	repo := &rateLimitAccountRepoStub{}
	incidents := &tkBridgePenaltyIncidentRecorder{}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAccountRuntimeBlocker(&runtimeBlockRecorder{})
	svc.SetAccountIncidentNotifier(incidents)

	account := newEdgeUS4KiroAccount()
	testSvc := &AccountTestService{
		kiroGatewayService: NewKiroGatewayService(upstream, nil),
		rateLimitService:   svc,
	}
	err := testSvc.testKiroAccountConnection(c, account, KiroDefaultTestModel, "hi")
	require.Error(t, err)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, []string{tkKiroQuotaLimitIncidentReason}, incidents.reasons)
	require.Contains(t, incidents.details[0], "402")
}
