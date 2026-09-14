package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestDetectOpenAIUsagePolicy(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		msg     string
		payload string
		want    bool
	}{
		{
			name:    "usage_policy_phrase",
			payload: `{"error":{"message":"Invalid prompt: your prompt was flagged as violating our usage policy."}}`,
			want:    true,
		},
		{
			name:    "violating_our_usage",
			msg:     "Your prompt is violating our usage guidelines",
			payload: `{}`,
			want:    true,
		},
		{
			name:    "invalid_prompt_with_violat",
			payload: `{"error":{"code":"invalid_prompt","message":"Prompt violates safety rules"}}`,
			want:    true,
		},
		{
			name:    "bare_policy_not_enough",
			payload: `{"error":{"code":"content_policy","message":"blocked by policy"}}`,
			want:    false,
		},
		{
			name:    "cyber_code_is_not_usage",
			payload: `{"error":{"code":"cyber_policy","message":"Request blocked by content policy."}}`,
			want:    false,
		},
		{
			name:    "echoed_user_prompt_in_body_not_enough",
			payload: `{"error":{"code":"invalid_request_error","message":"bad request"},"echo":{"prompt":"explain usage policy to me"}}`,
			want:    false,
		},
		{
			name:    "wrapper_msg_still_reads_body_message",
			msg:     "wrapped upstream failure",
			payload: `{"error":{"message":"Invalid prompt: your prompt was flagged as violating our usage policy."}}`,
			want:    true,
		},
		{
			name:    "empty",
			payload: `{}`,
			want:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			hit, _ := detectOpenAIUsagePolicy(tc.msg, []byte(tc.payload))
			require.Equal(t, tc.want, hit)
		})
	}
}

func TestMarkOpsUsagePolicyFirstWinsAndClear(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)

	MarkOpsUsagePolicy(c, UsagePolicyMark{Message: "first", UpstreamStatus: 400})
	MarkOpsUsagePolicy(c, UsagePolicyMark{Message: "second", UpstreamStatus: 500})
	got := GetOpsUsagePolicy(c)
	require.NotNil(t, got)
	require.Equal(t, "first", got.Message)
	require.Equal(t, "usage_policy", got.Code)

	ClearOpsUsagePolicy(c)
	require.Nil(t, GetOpsUsagePolicy(c))

	MarkOpsUsagePolicy(c, UsagePolicyMark{Message: "again", UpstreamStatus: 400})
	require.Equal(t, "again", GetOpsUsagePolicy(c).Message)
}

func TestOpenAIUsagePolicyNeverFailsOver(t *testing.T) {
	body := []byte(`{"error":{"message":"Invalid prompt: your prompt was flagged as violating our usage policy."}}`)
	svc := &OpenAIGatewayService{}

	require.True(t, isOpenAISafetySessionBlockFault("", body))
	require.False(t, (&OpenAIGatewayService{}).shouldFailoverOpenAIUpstreamResponse(nil, http.StatusBadRequest, "", body))
	require.False(t, svc.shouldFailoverOpenAIUpstreamResponse(newOpenAIUpstreamErrorTestAccount(), http.StatusBadGateway, "wrapped", body))
	require.False(t, shouldFailoverOpenAIPassthroughResponse(&Account{Type: AccountTypeOAuth}, http.StatusBadGateway, body))
	require.False(t, openAIStreamFailedEventShouldFailover(body, "violating our usage policy"))
	require.False(t, openAIStreamErrorEventShouldFailover(body, "violating our usage policy"))
}

func TestMarkOpenAISafetyPolicyEventPrefersCyber(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(nil)
	payload := []byte(`{"error":{"code":"cyber_policy","message":"blocked by cyber and also usage policy"}}`)
	kind := markOpenAISafetyPolicyEvent(c, payload, 400, nil)
	require.Equal(t, "cyber_policy", kind)
	require.NotNil(t, GetOpsCyberPolicy(c))
	require.Nil(t, GetOpsUsagePolicy(c))
}

func TestGetCyberSessionBlockRuntimeDefaultsOnWhenUnset(t *testing.T) {
	svc := &SettingService{settingRepo: &fakeSettingRepo{vals: map[string]string{}}}
	enabled, ttl := svc.GetCyberSessionBlockRuntime(t.Context())
	require.True(t, enabled, "unset key must default to enabled")
	require.Equal(t, time.Hour, ttl)

	svc2 := &SettingService{settingRepo: &fakeSettingRepo{vals: map[string]string{
		SettingKeyCyberSessionBlockEnabled: "false",
	}}}
	enabled, _ = svc2.GetCyberSessionBlockRuntime(t.Context())
	require.False(t, enabled, "explicit false must stay off")
}

func TestOpenAIUsagePolicyPreservesCredentialFaults(t *testing.T) {
	body := []byte(`{"error":{"code":"account_deactivated","message":"Your account has been deactivated due to a violation of our usage policy."}}`)
	hit, _ := detectOpenAIUsagePolicy("", body)
	require.False(t, hit, "credential deactivation must not isolate the caller session")
	require.True(t, (&OpenAIGatewayService{}).shouldFailoverOpenAIUpstreamResponse(newOpenAIUpstreamErrorTestAccount(), http.StatusForbidden, "", body))
	require.True(t, shouldFailoverOpenAIPassthroughResponse(&Account{Type: AccountTypeOAuth}, http.StatusForbidden, body))
	require.True(t, openAIStreamFailedEventShouldFailover(body, ""))
}

func TestOpenAIPassthroughSafetyPolicyKeepsClientError(t *testing.T) {
	for _, code := range []string{"cyber_policy", "invalid_prompt"} {
		for _, status := range []int{400, 403, 502} {
			t.Run(code+http.StatusText(status), func(t *testing.T) {
				rec := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(rec)
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
				upstreamError := gin.H{"code": code, "type": "invalid_request_error", "message": "Invalid prompt: violating our usage policy"}
				envelope := gin.H{"error": upstreamError, "internal": "private-provider-detail"}
				if status == http.StatusBadGateway {
					envelope = gin.H{"response": gin.H{"error": upstreamError}, "internal": "private-provider-detail"}
				}
				body, err := json.Marshal(envelope)
				require.NoError(t, err)
				svc := &OpenAIGatewayService{}
				err = svc.handleErrorResponsePassthrough(context.Background(), &http.Response{StatusCode: status, Header: http.Header{"Set-Cookie": []string{"private-cookie"}}}, c, &Account{ID: 1, Platform: PlatformOpenAI}, nil, body)
				require.Error(t, err)
				require.Equal(t, status, c.Writer.Status())
				require.JSONEq(t, `{"error":{"code":"`+code+`","type":"invalid_request_error","message":"Invalid prompt: violating our usage policy"}}`, rec.Body.String())
				require.Empty(t, rec.Header().Get("Set-Cookie"))
			})
		}
	}
}

func TestCyberSessionBlockSettingsMatchRuntime(t *testing.T) {
	for _, value := range []string{"", "true", "false", "0", "off", "disabled", "no"} {
		t.Run(value, func(t *testing.T) {
			settings := map[string]string{SettingKeyCyberSessionBlockEnabled: value}
			svc := NewSettingService(&fakeSettingRepo{vals: settings}, &config.Config{})
			enabled, _ := svc.GetCyberSessionBlockRuntime(t.Context())
			require.Equal(t, svc.parseSettings(settings).CyberSessionBlockEnabled, enabled)
		})
	}
}
