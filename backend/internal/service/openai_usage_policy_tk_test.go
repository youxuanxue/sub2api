package service

import (
	"net/http"
	"testing"
	"time"

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
	require.False(t, shouldFailoverOpenAIUpstreamError(http.StatusBadRequest, "", body))
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
