//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tokenestimate"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// Contract: docs/approved/usage-estimate-fallback-ssot.md.

const (
	estimateAnswer   = "The answer is two hundred and five."
	estimateQuestion = "What is one hundred and two plus one hundred and three?"
)

type estimateCase struct {
	name, path, contentType, request, response string
	stream                                     bool
	clientUsage                                func(body string) (in, out int64)
}

func chatClientUsage(body string) (int64, int64) {
	u := lastEstimateSSEUsage(body, "usage")
	if !u.Exists() {
		u = gjson.Get(body, "usage")
	}
	return u.Get("prompt_tokens").Int(), u.Get("completion_tokens").Int()
}

func responsesClientUsage(body string) (int64, int64) {
	u := lastEstimateSSEUsage(body, "response.usage")
	if !u.Exists() {
		u = gjson.Get(body, "usage")
	}
	return u.Get("input_tokens").Int(), u.Get("output_tokens").Int()
}

func messagesClientUsage(body string) (int64, int64) {
	u := lastEstimateSSEUsage(body, "usage")
	if !u.Exists() {
		u = gjson.Get(body, "usage")
	}
	return u.Get("input_tokens").Int(), u.Get("output_tokens").Int()
}

func geminiClientUsage(body string) (int64, int64) {
	u := gjson.Get(body, "usageMetadata")
	return u.Get("promptTokenCount").Int(), u.Get("candidatesTokenCount").Int()
}

func lastEstimateSSEUsage(body, path string) gjson.Result {
	var usage gjson.Result
	for _, line := range strings.Split(body, "\n") {
		payload, ok := extractOpenAISSEDataLine(line)
		if !ok || strings.TrimSpace(payload) == "[DONE]" {
			continue
		}
		if u := gjson.Get(payload, path); u.IsObject() {
			usage = u
		}
	}
	return usage
}

var zeroUsageCases = []estimateCase{
	{
		name: "chat/non-stream", path: "/v1/chat/completions", contentType: "application/json",
		request:     `{"model":"m","messages":[{"role":"system","content":"You are a careful calculator."},{"role":"user","content":"` + estimateQuestion + `"}]}`,
		response:    `{"id":"x","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"` + estimateAnswer + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
		clientUsage: chatClientUsage,
	},
	{
		name: "chat/stream-without-usage", path: "/v1/chat/completions", contentType: "text/event-stream", stream: true,
		request: `{"model":"m","stream":true,"messages":[{"role":"user","content":"` + estimateQuestion + `"}]}`,
		response: "data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + estimateAnswer + "\"}}]}\n\n" +
			"data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n",
		clientUsage: chatClientUsage,
	},
	{
		name: "chat/stream-zero-usage-chunk", path: "/v1/chat/completions", contentType: "text/event-stream", stream: true,
		request: `{"model":"m","stream":true,"messages":[{"role":"user","content":"` + estimateQuestion + `"}]}`,
		response: "data: {\"id\":\"s2\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + estimateAnswer + "\"}}]}\n\n" +
			"data: {\"id\":\"s2\",\"object\":\"chat.completion.chunk\",\"model\":\"m\",\"choices\":[],\"usage\":{\"prompt_tokens\":0,\"completion_tokens\":0,\"total_tokens\":0}}\n\n" +
			"data: [DONE]\n\n",
		clientUsage: chatClientUsage,
	},
	{
		name: "responses/non-stream", path: "/v1/responses", contentType: "application/json",
		request:     `{"model":"m","input":"` + estimateQuestion + `"}`,
		response:    `{"id":"r","object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"` + estimateAnswer + `"}]}],"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0}}`,
		clientUsage: responsesClientUsage,
	},
	{
		name: "responses/stream", path: "/v1/responses", contentType: "text/event-stream", stream: true,
		request: `{"model":"m","stream":true,"input":[{"role":"user","content":[{"type":"input_text","text":"` + estimateQuestion + `"}]}]}`,
		response: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"" + estimateAnswer + "\"}\n\n" +
			"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"r\",\"status\":\"completed\",\"output\":[{\"type\":\"message\",\"content\":[{\"type\":\"output_text\",\"text\":\"" + estimateAnswer + "\"}]}],\"usage\":{\"input_tokens\":0,\"output_tokens\":0,\"total_tokens\":0}}}\n\n",
		clientUsage: responsesClientUsage,
	},
	{
		name: "messages/non-stream", path: "/v1/messages", contentType: "application/json",
		request:     `{"model":"claude-x","system":"You are a careful calculator.","messages":[{"role":"user","content":[{"type":"text","text":"` + estimateQuestion + `"}]}]}`,
		response:    `{"id":"msg","type":"message","role":"assistant","content":[{"type":"text","text":"` + estimateAnswer + `"}],"usage":{"input_tokens":0,"output_tokens":0}}`,
		clientUsage: messagesClientUsage,
	},
	{
		name: "messages/stream", path: "/v1/messages", contentType: "text/event-stream", stream: true,
		request: `{"model":"claude-x","stream":true,"messages":[{"role":"user","content":"` + estimateQuestion + `"}]}`,
		response: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg\",\"usage\":{\"input_tokens\":0,\"output_tokens\":0}}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"" + estimateAnswer + "\"}}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":0}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		clientUsage: messagesClientUsage,
	},
	{
		name: "gemini/non-stream", path: "/v1beta/models/gemini-x:generateContent", contentType: "application/json",
		request:     `{"systemInstruction":{"parts":[{"text":"You are a careful calculator."}]},"contents":[{"role":"user","parts":[{"text":"` + estimateQuestion + `"}]}]}`,
		response:    `{"candidates":[{"content":{"role":"model","parts":[{"text":"` + estimateAnswer + `"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}}`,
		clientUsage: geminiClientUsage,
	},
	{
		name: "gemini/stream", path: "/v1beta/models/gemini-x:streamGenerateContent", contentType: "text/event-stream", stream: true,
		request:  `{"contents":[{"role":"user","parts":[{"text":"` + estimateQuestion + `"}]}]}`,
		response: "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"" + estimateAnswer + "\"}]},\"finishReason\":\"STOP\"}]}\n\n",
		// Gemini streams carry usageMetadata on content chunks; the writer
		// does not hold them, so the estimate is billed but not echoed.
		clientUsage: nil,
	},
}

type estimateRun struct {
	body   string
	ctx    *gin.Context
	result *ForwardResult
	openai *OpenAIForwardResult
	billed bool
}

// runEstimateCase drives the real capture wrapper: the handler writes the
// upstream-shaped response, then calls the owner as the billing choke point
// does, before the handler returns and held bytes are released.
func runEstimateCase(t *testing.T, tc estimateCase, account *Account, useOpenAI bool, forwardErr error, mutate func(*gin.Context)) estimateRun {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var run estimateRun
	r := gin.New()
	r.Use(WithUsageEstimateCapture(func(c *gin.Context) { c.Next() }))
	r.POST("/*path", func(c *gin.Context) {
		run.ctx = c
		body, _ := io.ReadAll(c.Request.Body)
		c.Writer.Header().Set("Content-Type", tc.contentType)
		c.Writer.WriteHeader(http.StatusOK)
		_, _ = c.Writer.WriteString(tc.response)
		if tc.stream {
			c.Writer.Flush()
		}
		if mutate != nil {
			mutate(c)
		}
		if useOpenAI {
			run.openai = &OpenAIForwardResult{Model: "m", Stream: tc.stream, RequestID: "up-1"}
			run.billed = (&OpenAIGatewayService{}).ApplyOpenAIUsageEstimateFallback(context.Background(), c, account, run.openai, body, forwardErr)
		} else {
			run.result = &ForwardResult{Model: "m", Stream: tc.stream, RequestID: "up-1"}
			run.billed = (&GatewayService{}).ApplyUsageEstimateFallback(context.Background(), c, account, run.result, body, forwardErr)
		}
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.request)))
	run.body = rec.Body.String()
	return run
}

func usageEstimatedEvents(c *gin.Context) []*OpsUpstreamErrorEvent {
	v, _ := c.Get(OpsUpstreamErrorsKey)
	events, _ := v.([]*OpsUpstreamErrorEvent)
	var out []*OpsUpstreamErrorEvent
	for _, ev := range events {
		if ev != nil && ev.Kind == "usage_estimated" {
			out = append(out, ev)
		}
	}
	return out
}

func estimateTestAccount() *Account {
	return &Account{ID: 42, Name: "acct", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
}

func TestUsageEstimateZeroUsageBillsTagsAndFillsClientUsage(t *testing.T) {
	for _, tc := range zeroUsageCases {
		for _, useOpenAI := range []bool{false, true} {
			t.Run(tc.name, func(t *testing.T) {
				run := runEstimateCase(t, tc, estimateTestAccount(), useOpenAI, nil, nil)
				require.True(t, run.billed)
				var in, out int
				var tier string
				if useOpenAI {
					in, out, tier = run.openai.Usage.InputTokens, run.openai.Usage.OutputTokens, run.openai.BillingTier
				} else {
					in, out, tier = run.result.Usage.InputTokens, run.result.Usage.OutputTokens, run.result.BillingTier
				}
				require.Equal(t, tokenestimate.Count(estimateAnswer), out)
				require.Greater(t, in, 0)
				require.Equal(t, UsageEstimatedBillingTier, tier)
				require.Contains(t, run.body, estimateAnswer, "content reaches the client unchanged")
				if tc.clientUsage != nil {
					cin, cout := tc.clientUsage(run.body)
					require.EqualValues(t, in, cin, "client-visible input equals billed")
					require.EqualValues(t, out, cout, "client-visible output equals billed")
				}
				events := usageEstimatedEvents(run.ctx)
				require.Len(t, events, 1)
				require.Equal(t, int64(42), events[0].AccountID)
				require.Equal(t, PlatformAnthropic, events[0].Platform)
				require.Contains(t, events[0].Detail, "stream=")
				require.Contains(t, events[0].Detail, "protocol=")
			})
		}
	}
}

func TestUsageEstimateChatStreamOrder(t *testing.T) {
	run := runEstimateCase(t, zeroUsageCases[1], estimateTestAccount(), true, nil, nil)
	require.Less(t, strings.Index(run.body, `"usage"`), strings.LastIndex(run.body, "[DONE]"), "usage chunk precedes [DONE]")
	require.Equal(t, 1, strings.Count(run.body, `"usage"`))
	patched := runEstimateCase(t, zeroUsageCases[2], estimateTestAccount(), true, nil, nil)
	require.Equal(t, 1, strings.Count(patched.body, `"usage"`), "patch in place, do not add a second usage chunk")
}

func TestUsageEstimateRealUsageWins(t *testing.T) {
	tc := zeroUsageCases[0]
	run := runEstimateCase(t, tc, estimateTestAccount(), false, nil, nil)
	require.True(t, run.billed)

	// Real usage on the result: untouched, no event, client body unchanged.
	gin.SetMode(gin.TestMode)
	for _, real := range []ClaudeUsage{{InputTokens: 20}, {OutputTokens: 3}, {CacheReadInputTokens: 9}, {CacheCreationInputTokens: 1}, {ImageOutputTokens: 1}} {
		rec := httptest.NewRecorder()
		r := gin.New()
		r.Use(WithUsageEstimateCapture(func(c *gin.Context) { c.Next() }))
		var res *ForwardResult
		var c0 *gin.Context
		r.POST("/*p", func(c *gin.Context) {
			c0 = c
			c.Writer.Header().Set("Content-Type", "application/json")
			_, _ = c.Writer.WriteString(strings.Replace(tc.response, `"prompt_tokens":0`, `"prompt_tokens":20`, 1))
			res = &ForwardResult{Usage: real}
			require.False(t, (&GatewayService{}).ApplyUsageEstimateFallback(context.Background(), c, estimateTestAccount(), res, []byte(tc.request), nil))
		})
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, tc.path, strings.NewReader(tc.request)))
		require.Equal(t, real, res.Usage)
		require.Empty(t, res.BillingTier)
		require.Empty(t, usageEstimatedEvents(c0))
		require.Contains(t, rec.Body.String(), `"prompt_tokens":20`)
	}
	for _, real := range []OpenAIUsage{{InputTokens: 20}, {CacheReadInputTokens: 2}, {ImageInputTokens: 4}} {
		res := &OpenAIForwardResult{Usage: real}
		require.False(t, (&OpenAIGatewayService{}).ApplyOpenAIUsageEstimateFallback(context.Background(), run.ctx, estimateTestAccount(), res, nil, nil))
		require.Equal(t, real, res.Usage)
	}
}

func TestUsageEstimateEmptyOutputIsNotEstimated(t *testing.T) {
	cases := []estimateCase{
		{name: "chat", path: "/v1/chat/completions", contentType: "application/json", request: zeroUsageCases[0].request,
			response: `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":""},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`},
		{name: "chat-stream", path: "/v1/chat/completions", contentType: "text/event-stream", stream: true, request: zeroUsageCases[1].request,
			response: "data: {\"id\":\"s\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
		{name: "messages", path: "/v1/messages", contentType: "application/json", request: zeroUsageCases[5].request,
			response: `{"type":"message","content":[],"usage":{"input_tokens":0,"output_tokens":0}}`},
		{name: "gemini", path: "/v1beta/models/g:generateContent", contentType: "application/json", request: zeroUsageCases[7].request,
			response: `{"candidates":[{"content":{"parts":[]},"finishReason":"SAFETY"}]}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			run := runEstimateCase(t, tc, estimateTestAccount(), false, nil, nil)
			require.False(t, run.billed)
			require.Zero(t, run.result.Usage.InputTokens)
			require.Empty(t, run.result.BillingTier)
			require.Empty(t, usageEstimatedEvents(run.ctx))
		})
	}
}

func TestUsageEstimateSkipsErrorsDisconnectsMediaAndNativeEstimators(t *testing.T) {
	tc := zeroUsageCases[1]
	// Forward returned an error with a partial result.
	run := runEstimateCase(t, tc, estimateTestAccount(), true, errors.New("stream read"), nil)
	require.False(t, run.billed)
	// In-band stream error recorded by the forward path.
	run = runEstimateCase(t, tc, estimateTestAccount(), true, nil, func(c *gin.Context) {
		c.Set(OpsStreamErrorsKey, []OpsStreamError{{ErrType: "api_error"}})
	})
	require.False(t, run.billed)
	// Native estimators: Kiro and Gemini Web settle on their own.
	run = runEstimateCase(t, tc, &Account{ID: 1, Platform: PlatformKiro}, false, nil, nil)
	require.False(t, run.billed)
	geminiWeb := &Account{ID: 2, Platform: PlatformGemini, Type: AccountTypeAPIKey, Credentials: map[string]any{"gemini_web": map[string]any{}}}
	run = runEstimateCase(t, zeroUsageCases[7], geminiWeb, false, nil, nil)
	require.False(t, run.billed)
	require.Contains(t, run.body, `"promptTokenCount":0`, "native path client body untouched")
	// Media results are priced by their own units.
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	require.False(t, (&OpenAIGatewayService{}).ApplyOpenAIUsageEstimateFallback(context.Background(), c, estimateTestAccount(), &OpenAIForwardResult{ImageCount: 1}, nil, nil))
	require.False(t, (&OpenAIGatewayService{}).ApplyOpenAIUsageEstimateFallback(context.Background(), c, estimateTestAccount(), &OpenAIForwardResult{VideoCount: 1}, nil, nil))
	require.False(t, (&GatewayService{}).ApplyUsageEstimateFallback(context.Background(), c, estimateTestAccount(), &ForwardResult{AudioUsage: &AudioUsage{Mode: "tts"}}, nil, nil))
	require.False(t, (&OpenAIGatewayService{}).ApplyOpenAIUsageEstimateFallback(context.Background(), c, estimateTestAccount(), &OpenAIForwardResult{ClientDisconnect: true}, nil, nil))
	// No capture writer (non-text route): never estimates.
	require.False(t, (&OpenAIGatewayService{}).ApplyOpenAIUsageEstimateFallback(context.Background(), c, estimateTestAccount(), &OpenAIForwardResult{}, []byte(`{"input":"x"}`), nil))
}

func TestUsageEstimateCaptureSkipsNonTextRoutes(t *testing.T) {
	for _, path := range []string{"/v1/embeddings", "/v1/messages/count_tokens", "/v1/responses/input_tokens", "/v1/responses/compact", "/v1/images/generations", "/v1beta/models/g:countTokens"} {
		p, _ := usageEstimateProtocolForPath(path)
		require.Empty(t, p, path)
	}
	for path, want := range map[string]string{
		"/chat/completions": usageEstimateProtocolChat, "/backend-api/codex/responses": usageEstimateProtocolResponses,
		"/antigravity/v1/messages": usageEstimateProtocolMessages, "/antigravity/v1beta/models/g:streamGenerateContent": usageEstimateProtocolGemini,
	} {
		p, _ := usageEstimateProtocolForPath(path)
		require.Equal(t, want, p, path)
	}
}

// Failover guards compare writer sizes before and after a forward; bytes the
// capture writer holds must count as written.
func TestUsageEstimateCaptureReportsHeldBytesAsWritten(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(WithUsageEstimateCapture(func(c *gin.Context) { c.Next() }))
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		before := c.Writer.Size()
		c.Writer.Header().Set("Content-Type", "application/json")
		_, _ = c.Writer.Write([]byte(`{"choices":[]}`))
		require.NotEqual(t, before, c.Writer.Size())
		require.True(t, c.Writer.Written())
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(nil)))
	require.Equal(t, `{"choices":[]}`, rec.Body.String())
}

// The stream must not be buffered before its terminal usage frame.
func TestUsageEstimateCaptureStreamsContentImmediately(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(WithUsageEstimateCapture(func(c *gin.Context) { c.Next() }))
	var seen string
	rec := httptest.NewRecorder()
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}],\"usage\":{\"prompt_tokens\":1}}\n\n")
		c.Writer.Flush()
		seen = rec.Body.String()
	})
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	require.Contains(t, seen, `"content":"hi"`)
}

func TestUsageEstimateBilledAndPersistedOnBothRecordUsagePaths(t *testing.T) {
	// OpenAI-compatible RecordUsage persists the tier and charges the estimate.
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	billingRepo := &openAIRecordUsageBillingRepoStub{result: &UsageBillingApplyResult{Applied: true}}
	svc := newOpenAIRecordUsageServiceWithBillingRepoForTest(usageRepo, billingRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
		Result: &OpenAIForwardResult{
			RequestID: "usage_estimated", Usage: OpenAIUsage{InputTokens: 120, OutputTokens: 40},
			BillingTier: UsageEstimatedBillingTier, Model: "gpt-5.1", Duration: time.Second,
		},
		APIKey:        &APIKey{ID: 1000, Quota: 100, Group: &Group{RateMultiplier: 1}},
		User:          &User{ID: 2000},
		Account:       &Account{ID: 3000, Type: AccountTypeAPIKey},
		APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
	})
	require.NoError(t, err)
	require.NotNil(t, usageRepo.lastLog.BillingTier)
	require.Equal(t, UsageEstimatedBillingTier, *usageRepo.lastLog.BillingTier)
	require.Equal(t, 120, usageRepo.lastLog.InputTokens)
	require.Greater(t, usageRepo.lastLog.ActualCost, 0.0)
	require.Greater(t, billingRepo.lastCmd.BalanceCost, 0.0)

	// Other OpenAI-path tiers keep their historical (unpersisted) behavior.
	require.Nil(t, openAIUsageLogBillingTier(&OpenAIForwardResult{BillingTier: "kiro-estimated"}))
	require.Nil(t, openAIUsageLogBillingTier(&OpenAIForwardResult{}))

	// Claude-gateway RecordUsage already persists result.BillingTier.
	gwRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	gw := newGatewayRecordUsageServiceForTest(gwRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
	err = gw.RecordUsage(context.Background(), &RecordUsageInput{
		Result: &ForwardResult{
			RequestID: "usage_estimated_gw", Usage: ClaudeUsage{InputTokens: 120, OutputTokens: 40},
			BillingTier: UsageEstimatedBillingTier, Model: "claude-sonnet-4", Duration: time.Second,
		},
		APIKey:        &APIKey{ID: 501, Quota: 100},
		User:          &User{ID: 601},
		Account:       &Account{ID: 701},
		APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
	})
	require.NoError(t, err)
	require.NotNil(t, gwRepo.lastLog.BillingTier)
	require.Equal(t, UsageEstimatedBillingTier, *gwRepo.lastLog.BillingTier)
	require.Greater(t, gwRepo.lastLog.TotalCost, 0.0)
}

// billing_tier is a label: the same tokens cost the same with or without it.
func TestUsageEstimateTierDoesNotChangePrice(t *testing.T) {
	cost := func(tier string) float64 {
		repo := &openAIRecordUsageLogRepoStub{inserted: true}
		gw := newGatewayRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{})
		require.NoError(t, gw.RecordUsage(context.Background(), &RecordUsageInput{
			Result:        &ForwardResult{RequestID: "tier-" + tier, Usage: ClaudeUsage{InputTokens: 1000, OutputTokens: 500}, BillingTier: tier, Model: "claude-sonnet-4", Duration: time.Second},
			APIKey:        &APIKey{ID: 1, Quota: 100},
			User:          &User{ID: 2},
			Account:       &Account{ID: 3},
			APIKeyService: &openAIRecordUsageAPIKeyQuotaStub{},
		}))
		return repo.lastLog.TotalCost
	}
	require.Equal(t, cost(""), cost(UsageEstimatedBillingTier))
}

// A writer that flushes without an event-stream Content-Type is streaming:
// the capture writer must release immediately, and still bill the estimate.
func TestUsageEstimateCaptureFlushReleasesHeldBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(WithUsageEstimateCapture(func(c *gin.Context) { c.Next() }))
	rec := httptest.NewRecorder()
	var seen string
	var billed bool
	r.POST("/v1/chat/completions", func(c *gin.Context) {
		_, _ = c.Writer.WriteString(zeroUsageCases[0].response)
		c.Writer.Flush()
		seen = rec.Body.String()
		billed = (&OpenAIGatewayService{}).ApplyOpenAIUsageEstimateFallback(context.Background(), c, estimateTestAccount(), &OpenAIForwardResult{}, []byte(zeroUsageCases[0].request), nil)
	})
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil))
	require.Contains(t, seen, estimateAnswer)
	require.True(t, billed)
	require.Contains(t, rec.Body.String(), `"prompt_tokens":0`, "already-sent body is not rewritten")
}

// Volc Agent Plan kimi-k3 (cn-prod 2026-11-04) now rides the gateway-wide
// owner: real raw-chat forward, then the choke point hook, as the handler does.
func TestUsageEstimateVolcKimiRawChatThroughOwner(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		stream  bool
		payload string
	}{
		{false, `{"id":"x","object":"chat.completion","model":"kimi-k3","choices":[{"index":0,"message":{"role":"assistant","content":"` + estimateAnswer + `"},"finish_reason":"stop"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`},
		{true, "data: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"" + estimateAnswer + "\"}}]}\n\ndata: {\"id\":\"s1\",\"object\":\"chat.completion.chunk\",\"model\":\"kimi-k3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"},
	} {
		contentType := "application/json"
		if tc.stream {
			contentType = "text/event-stream"
		}
		reqBody := []byte(`{"model":"kimi-k3","stream":` + map[bool]string{true: "true", false: "false"}[tc.stream] + `,"messages":[{"role":"user","content":"` + estimateQuestion + `"}]}`)
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(tc.payload))}}
		svc := &OpenAIGatewayService{cfg: rawChatCompletionsTestConfig(), httpUpstream: upstream}
		account := volcAgentPlanKimiAccountForTest()
		var result *OpenAIForwardResult
		var ctx *gin.Context
		r := gin.New()
		r.Use(WithUsageEstimateCapture(func(c *gin.Context) { c.Next() }))
		r.POST("/v1/chat/completions", func(c *gin.Context) {
			ctx = c
			var err error
			result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, reqBody, "", "")
			require.NoError(t, err)
			require.True(t, (&OpenAIGatewayService{}).ApplyOpenAIUsageEstimateFallback(context.Background(), c, account, result, reqBody, err))
		})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBody)))
		require.Equal(t, UsageEstimatedBillingTier, result.BillingTier)
		require.Equal(t, tokenestimate.Count(estimateAnswer), result.Usage.OutputTokens)
		require.Equal(t, tokenestimate.Count(estimateQuestion), result.Usage.InputTokens, "outbound body counted")
		in, out := chatClientUsage(rec.Body.String())
		require.EqualValues(t, result.Usage.InputTokens, in)
		require.EqualValues(t, result.Usage.OutputTokens, out)
		events := usageEstimatedEvents(ctx)
		require.Len(t, events, 1)
		require.Equal(t, PlatformNewAPI, events[0].Platform)
	}
}

// Carried from R-001 (110bb9e1f): client usage.cost, the precomputed
// settlement and the usage_logs cost come from one computation on every chat
// estimate shape, including the usage chunk the owner inserts when the
// upstream stream carried none.
func TestUsageEstimateCostMatchesBilling(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range zeroUsageCases[:3] {
		t.Run(tc.name, func(t *testing.T) {
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			if svc.cfg == nil {
				svc.cfg = rawChatCompletionsTestConfig()
			}
			svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": {tc.contentType}},
				Body:       io.NopCloser(strings.NewReader(strings.ReplaceAll(tc.response, `"model":"m"`, `"model":"kimi-k3"`))),
			}}
			apiKey := openAIRecordUsageAPIKeyWithGroup(svc, 637, false)
			apiKey.User = &User{ID: 61, Balance: 100}
			apiKey.GroupID = &apiKey.Group.ID
			apiKey.Group.Platform = PlatformOpenAI
			account := volcAgentPlanKimiAccountForTest()
			body := []byte(strings.Replace(tc.request, `"model":"m"`, `"model":"kimi-k3"`, 1))

			var result *OpenAIForwardResult
			r := gin.New()
			r.Use(WithUsageEstimateCapture(func(c *gin.Context) { c.Next() }))
			r.POST("/v1/chat/completions", func(c *gin.Context) {
				c.Set("api_key", apiKey)
				BindResponseUsageCostBillingWithTier(c, time.Now(), ChannelUsageFields{}, "")
				var err error
				result, err = svc.ForwardAsChatCompletions(context.Background(), c, account, body, "", "")
				require.NoError(t, err)
				require.True(t, svc.ApplyOpenAIUsageEstimateFallback(context.Background(), c, account, result, body, err))
			})
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))

			require.Equal(t, UsageEstimatedBillingTier, result.BillingTier)
			require.NotNil(t, result.PrecomputedCost, "estimated usage must be priced before billing")
			wire := lastEstimateSSEUsage(rec.Body.String(), "usage")
			if !tc.stream {
				wire = gjson.Get(rec.Body.String(), "usage")
			}
			require.True(t, wire.Get("cost").Exists(), "client usage must carry usage.cost: %s", rec.Body.String())
			require.Positive(t, wire.Get("cost").Float())
			require.InDelta(t, result.PrecomputedCost.TotalCost, wire.Get("cost").Float(), 1e-12)

			result.RequestID = "usage-estimate-cost-" + tc.name
			require.NoError(t, svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{Result: result, APIKey: apiKey, User: apiKey.User, Account: account}))
			require.NotNil(t, usageRepo.lastLog)
			require.InDelta(t, wire.Get("cost").Float(), usageRepo.lastLog.TotalCost, 1e-12)
		})
	}
}
