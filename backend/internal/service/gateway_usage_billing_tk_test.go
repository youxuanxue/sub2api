//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTkRecordAvailabilitySuccessOutcome_NilSafe(t *testing.T) {
	s := &GatewayService{}
	require.NotPanics(t, func() {
		s.tkRecordAvailabilitySuccessOutcome(context.Background(), nil, nil)
	})
}

func TestTkRecordAvailabilitySuccessOutcome_RecordsSuccess(t *testing.T) {
	avail, repo, _ := newAvailabilityTestService(t)
	s := &GatewayService{tkPricingAvailability: avail}
	account := &Account{ID: 9, Platform: PlatformAnthropic}
	result := &ForwardResult{UpstreamModel: "claude-sonnet-4-5"}

	s.tkRecordAvailabilitySuccessOutcome(context.Background(), account, result)

	st, err := repo.Get(context.Background(), PlatformAnthropic, "claude-sonnet-4-5")
	require.NoError(t, err)
	require.Equal(t, AvailabilityStatusOK, st.Status,
		"success outcome companion must feed the availability owner")
}

func TestTkRecordUsagePostCostObservability_NilSafe(t *testing.T) {
	s := &GatewayService{}
	require.NotPanics(t, func() {
		s.tkRecordUsagePostCostObservability(nil, &ForwardResult{}, nil, "m", "m", 1, 1)
	})
}

func TestTkPrepareCountTokensAnthropicBody_NilAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	s := &GatewayService{
		settingService: &SettingService{},
	}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}]}`)
	stored := body
	replaceBody := func(next []byte) error {
		stored = append([]byte(nil), next...)
		return nil
	}
	getBody := func() []byte { return stored }

	next, model, err := s.tkPrepareCountTokensAnthropicBody(
		context.Background(), c, nil, body, "claude-sonnet-4-5", replaceBody, getBody,
	)
	require.NoError(t, err)
	require.Equal(t, "claude-sonnet-4-5", model)
	require.NotEmpty(t, next)
}
