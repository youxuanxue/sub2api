package service

import (
	"context"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFirstOutputObservationRecordsActualBudgetAndRecovery(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{OpenAIFirstOutputTimeoutSeconds: 300}}}
	account := &Account{ID: 1, Platform: PlatformOpenAI}
	BeginOpsFirstOutputAttempt(c)
	budget := svc.openAIFirstOutputTimeout("medium", false)
	reason := firstOutputBudgetReason(c, []byte(`{"input":"hi"}`), "medium")
	RecordOpsFirstOutputBudget(c, account, budget, reason)
	_ = svc.newOpenAIFirstOutputTimeoutError(context.Background(), c, account, nil, "direct/no_proxy", time.Now(), "model", "medium", budget, "semantic_output", nil)
	// Failed execution followed by a successful one, with multiple error events
	// in the first execution: neither event count nor switch count is the denominator.
	appendOpsUpstreamError(c, OpsUpstreamErrorEvent{Kind: "http_error", Message: "additional diagnostic"})
	BeginOpsFirstOutputAttempt(c)
	require.True(t, FinalizeOpsFirstOutputEvaluation(c, "recovered"))
	raw, _ := c.Get(OpsUpstreamErrorsKey)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Equal(t, int64(300000), events[0].BudgetMS)
	require.Equal(t, "default", events[0].BudgetReason) // 300 seconds does not imply compact.
	require.Equal(t, 2, events[0].AttemptCount)
	require.Equal(t, "recovered", events[0].TimeoutOutcome)
	entry := &OpsInsertErrorLogInput{UpstreamErrors: events}
	require.NoError(t, SanitizeOpsUpstreamErrorsForQueue(entry))
	persisted, err := ParseOpsUpstreamErrors(*entry.UpstreamErrorsJSON)
	require.NoError(t, err)
	require.Equal(t, "recovered", persisted[0].TimeoutOutcome)
	require.Equal(t, int64(300000), persisted[0].BudgetMS)
}

func TestFirstOutputBudgetReasonUsesSelectionInputs(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses/compact", nil)
	require.Equal(t, "compact", firstOutputBudgetReason(c, []byte(`{}`), "medium"))
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	require.Equal(t, "large_body", firstOutputBudgetReason(c, []byte(strings.Repeat(" ", openAIFirstOutputLargeBodyBytes)), "medium"))
	require.Equal(t, "high_effort", firstOutputBudgetReason(c, []byte(`{}`), "xhigh"))
	require.Equal(t, "high_effort", firstOutputBudgetReason(c, []byte(`{"model":"gpt-6-astra"}`), "medium"))
	require.Equal(t, "high_effort", firstOutputBudgetReason(c, []byte(`{"model":"gpt-5.6-sol"}`), "medium"))
	require.Equal(t, "default", firstOutputBudgetReason(c, []byte(`{}`), "medium"))
	require.True(t, firstOutputBudgetForcesHigh("compact"))
	require.True(t, firstOutputBudgetForcesHigh("large_body"))
	require.True(t, firstOutputBudgetForcesHigh("high_effort"))
	require.False(t, firstOutputBudgetForcesHigh("default"))
	require.False(t, firstOutputBudgetForcesHigh("newapi_inherit"))
}

func TestUniversalFirstOutputObservationIgnoresCallerCancel(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	account := &Account{ID: 1, Platform: PlatformAnthropic}
	BeginOpsFirstOutputAttempt(c)
	RecordOpsFirstOutputBudget(c, account, time.Second, "default")
	caller, cancel := context.WithCancel(context.Background())
	cancel()
	guarded, g := armStreamFirstOutputGuard(caller, time.Second)
	defer g.finish()
	require.NoError(t, streamFirstOutputFailoverIfBudgetFired(c, g, guarded, caller))
	_, found := c.Get(OpsUpstreamErrorsKey)
	require.False(t, found)
	// The genuine guard expiry gets one structured event despite repeated checks.
	guarded, g = armStreamFirstOutputGuard(context.Background(), time.Millisecond)
	defer g.finish()
	<-guarded.Done()
	require.Error(t, streamFirstOutputFailoverIfBudgetFired(c, g, guarded, context.Background()))
	require.Error(t, streamFirstOutputFailoverIfBudgetFired(c, g, guarded, context.Background()))
	raw, _ := c.Get(OpsUpstreamErrorsKey)
	events, ok := raw.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	require.Len(t, events, 1)
	require.Equal(t, "default", events[0].BudgetReason)
	require.Equal(t, int64(1000), events[0].BudgetMS)
}
