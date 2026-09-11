package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAIStreamCapacityRuleCoolsOnlyKnownModel(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, frame := range []struct{ name, payload string }{
		{"response.failed", `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."},"instructions":"private_input_keyword"}}`},
		{"error", `{"type":"error","error":{"code":"server_is_overloaded","message":"Our servers are currently overloaded. Please try again later."},"instructions":"private_input_keyword"}`},
	} {
		for _, tc := range []struct {
			name           string
			enabled        bool
			keyword, model string
			match          bool
		}{
			{"explicit overload rule", true, "server_is_overloaded", "gpt-5.4", true},
			{"default retains request local retry", false, "server_is_overloaded", "gpt-5.4", false},
			{"unmatched rule retains request local retry", true, "different_failure", "gpt-5.4", false},
			{"unknown model never blocks whole account", true, "server_is_overloaded", "", false},
			{"echoed input does not match rule", true, "private_input_keyword", "gpt-5.4", false},
		} {
			t.Run(frame.name+"/"+tc.name, func(t *testing.T) {
				repo := &passthroughTempUnschedRepo{}
				svc := &OpenAIGatewayService{rateLimitService: &RateLimitService{accountRepo: repo}}
				account := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeOAuth, Status: StatusActive, Schedulable: true, Credentials: map[string]any{
					"temp_unschedulable_enabled": tc.enabled,
					"temp_unschedulable_rules":   []any{map[string]any{"error_code": 503, "keywords": []any{tc.keyword}, "duration_minutes": 2}},
				}}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				payload := []byte(frame.payload)
				err := svc.newOpenAIStreamFailoverErrorWithModel(c, account, false, "upstream-id", payload, "Our servers are currently overloaded. Please try again later.", tc.model)
				require.Equal(t, http.StatusServiceUnavailable, err.StatusCode)
				require.Empty(t, repo.tempUnschedCalls, "model errors must not block every model on the account")
				require.True(t, account.Schedulable)
				if tc.match {
					require.Equal(t, []string{tc.model}, repo.modelRateLimitScopes)
					require.Contains(t, repo.modelRateLimitReasons[0], "server_is_overloaded")
					require.False(t, err.RetryableOnSameAccount, "the matched model is cooling, switch accounts immediately")
					require.False(t, err.RequestScopedTransient)
				} else {
					require.Empty(t, repo.modelRateLimitScopes)
					require.True(t, err.RetryableOnSameAccount)
					require.True(t, err.RequestScopedTransient)
				}
			})
		}
	}
}
