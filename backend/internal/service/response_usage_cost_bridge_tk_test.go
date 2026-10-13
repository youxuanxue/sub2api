//go:build unit

package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestBridgeChatUsageCost_AliAdaptorAndLedger(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint("stream=", stream), func(t *testing.T) {
			const usage = `"usage":{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120,"prompt_tokens_details":{"cached_tokens":40},"cost":987}`
			supplier := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/compatible-mode/v1/chat/completions" {
					http.Error(w, "wrong adaptor path", http.StatusNotFound)
					return
				}
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"id\":\"test\",\"model\":\"glm-5.3\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"OK\"},\"finish_reason\":null}]}\n\n")
					w.(http.Flusher).Flush()
					fmt.Fprintf(w, "data: {\"id\":\"test\",\"model\":\"glm-5.3\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: {\"id\":\"test\",\"model\":\"glm-5.3\",\"choices\":[],%s}\n\ndata: [DONE]\n\n", usage)
				} else {
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"id":"test","model":"glm-5.3","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}],%s}`, usage)
				}
			}))
			defer supplier.Close()
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			apiKey := openAIRecordUsageAPIKeyWithGroup(svc, 637, false)
			apiKey.User = &User{ID: 61, Balance: 100}
			apiKey.GroupID = &apiKey.Group.ID
			apiKey.Group.Platform = PlatformOpenAI
			apiKey.Group.RateMultiplier = 0.3
			apiKey.RoutingMode = RoutingModeUniversal
			account := &Account{ID: 113, Platform: PlatformNewAPI, Type: AccountTypeAPIKey, ChannelType: newapiconstant.ChannelTypeAli,
				Credentials: map[string]any{"base_url": supplier.URL, "api_key": "test-key"}}
			body, err := json.Marshal(gin.H{"model": "glm-5.3", "messages": []gin.H{{"role": "user", "content": "hi"}}, "stream": stream, "stream_options": gin.H{"include_usage": true}})
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			ctx := WithUniversalKeyRouting(context.Background())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)).WithContext(ctx)
			c.Request.Header.Set("Content-Type", "application/json")
			c.Set("api_key", apiKey)
			BindResponseUsageCostBillingWithTier(c, time.Now(), ChannelUsageFields{}, "")
			result, err := svc.ForwardAsChatCompletionsDispatched(ctx, c, account, body, "", "")
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, 100, result.Usage.InputTokens)
			require.Equal(t, 20, result.Usage.OutputTokens)
			require.Equal(t, 40, result.Usage.CacheReadInputTokens)
			require.NotNil(t, result.PrecomputedCost)
			var wireCost float64
			if stream {
				count := 0
				for _, line := range strings.Split(rec.Body.String(), "\n") {
					payload, _ := extractOpenAISSEDataLine(line)
					if cost := gjson.Get(payload, "usage.cost"); cost.Exists() {
						count++
						wireCost = cost.Float()
					}
				}
				require.Equal(t, 1, count, rec.Body.String())
				require.True(t, strings.HasSuffix(strings.TrimSpace(rec.Body.String()), "data: [DONE]"))
			} else {
				wireCost = gjson.GetBytes(rec.Body.Bytes(), "usage.cost").Float()
				require.Equal(t, "OK", gjson.GetBytes(rec.Body.Bytes(), "choices.0.message.content").String())
			}
			require.Positive(t, wireCost)
			require.InDelta(t, result.PrecomputedCost.TotalCost, wireCost, 1e-12)
			require.InDelta(t, wireCost*0.3, result.PrecomputedCost.ActualCost, 1e-12)
			result.RequestID = "bridge-cost-test"
			err = svc.RecordUsage(ctx, &OpenAIRecordUsageInput{Result: result, APIKey: apiKey, User: apiKey.User, Account: account})
			require.NoError(t, err)
			require.NotNil(t, usageRepo.lastLog)
			require.InDelta(t, wireCost, usageRepo.lastLog.TotalCost, 1e-12)
			require.InDelta(t, wireCost*0.3, usageRepo.lastLog.ActualCost, 1e-12)
		})
	}
}

func TestBridgeUsageCostWriter_StreamFragmentsAndFlush(t *testing.T) {
	const content = "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}],\"usage\":null}\n\n"
	const usage = "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"cost\":999}}\r\n\r\n"
	const done = "data: [DONE]\n\n"
	for _, split := range []int{0, 1, len(content) + 20, len(content) + len(usage) + len(done)} {
		t.Run(fmt.Sprint(split), func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			w := &bridgeUsageCostWriter{ResponseWriter: c.Writer, stream: true}
			wire := content + usage + done
			_, err := w.WriteString(wire[:split])
			require.NoError(t, err)
			w.Flush()
			_, err = w.WriteString(wire[split:])
			require.NoError(t, err)
			w.Flush()
			require.Equal(t, content, rec.Body.String(), "content must stream before settlement, usage must wait")
			cost := &CostBreakdown{TotalCost: 0.25, ActualCost: 0.075}
			got, err := w.finish(func() *CostBreakdown { return cost })
			require.NoError(t, err)
			require.Same(t, cost, got)
			require.Equal(t, content+strings.Replace(usage, "999", "0.25", 1)+done, rec.Body.String())
		})
	}
}

func TestBridgeUsageCostWriter_NoInventedUsageAndErrorPassthrough(t *testing.T) {
	for _, body := range []string{`{"choices":[]}`, `{"usage":null}`, `{"usage":"invalid"}`, `{"usage":{"prompt_tokens":3}}`} {
		for _, stream := range []bool{false, true} {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			w := &bridgeUsageCostWriter{ResponseWriter: c.Writer, stream: stream}
			wire := body
			if stream {
				wire = "data: " + body + "\n\ndata: [DONE]\n\n"
			}
			_, err := w.WriteString(wire)
			require.NoError(t, err)
			// An unsuccessful dispatch has no settled result even if a usage
			// frame was partially written; release its original bytes.
			cost, err := w.finish(nil)
			require.NoError(t, err)
			require.Nil(t, cost)
			require.Equal(t, wire, rec.Body.String())
		}
	}
	for _, body := range []string{`{"usage":null}`, `{"usage":[]}`, `{"usage":7}`, `{"message":{"usage":null}}`} {
		require.Equal(t, body, string(InjectUsageCostJSON([]byte(body), 0.5)))
	}
}

func TestBridgeUsageCostWriter_BoundedBufferPassthrough(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := &bridgeUsageCostWriter{ResponseWriter: c.Writer}
	body := `{"usage":{"prompt_tokens":3},"content":"` + strings.Repeat("x", maxBridgeUsageCostBuffer) + `"}`
	_, err := w.WriteString(body[:30])
	require.NoError(t, err)
	_, err = w.WriteString(body[30:])
	require.NoError(t, err)
	cost, err := w.finish(func() *CostBreakdown { t.Fatal("oversized response must not settle"); return nil })
	require.NoError(t, err)
	require.Nil(t, cost)
	require.Equal(t, body, rec.Body.String())
}

func TestBridgeUsageCostWriter_JSONLengthAndUnpricedUsage(t *testing.T) {
	for _, body := range []string{`{"usage":null}`, `{"choices":[]}`, `{"usage":{"prompt_tokens":3}}`} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		w := &bridgeUsageCostWriter{ResponseWriter: c.Writer}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.WriteHeaderNow()
		_, err := w.WriteString(body)
		require.NoError(t, err)
		called := false
		cost, err := w.finish(func() *CostBreakdown { called = true; return nil })
		require.NoError(t, err)
		require.Nil(t, cost)
		require.Equal(t, gjson.Get(body, "usage").IsObject(), called)
		require.Equal(t, body, rec.Body.String())
		require.Empty(t, rec.Result().Header.Get("Content-Length"))
	}
}

type disconnectedUsageCostWriter struct{ gin.ResponseWriter }

func (w disconnectedUsageCostWriter) Write([]byte) (int, error) {
	return 0, fmt.Errorf("client disconnected")
}

func TestBridgeUsageCostWriter_ClientDisconnectKeepsSettledBilling(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := &bridgeUsageCostWriter{ResponseWriter: disconnectedUsageCostWriter{c.Writer}}
	_, err := w.WriteString(`{"usage":{"prompt_tokens":3}}`)
	require.NoError(t, err)
	want := &CostBreakdown{TotalCost: 0.1, ActualCost: 0.03}
	cost, err := w.finish(func() *CostBreakdown { return want })
	require.EqualError(t, err, "client disconnected")
	require.Same(t, want, cost, "completed upstream usage remains available for billing after a client disconnect")
	_, err = w.finish(nil)
	require.NoError(t, err, "deferred cleanup must not retry or duplicate a failed write")
}

func TestBridgeUsageCostWriter_AdaptorWireFormatWins(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := &bridgeUsageCostWriter{ResponseWriter: c.Writer, stream: false}
	w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
	content := "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n"
	_, err := w.WriteString(content + "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3}}\n\ndata: [DONE]\n\n")
	require.NoError(t, err)
	require.Equal(t, content, rec.Body.String(), "adaptor-forced SSE must still stream content immediately")
	_, err = w.finish(func() *CostBreakdown { return &CostBreakdown{TotalCost: 0.1} })
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `"usage":{"prompt_tokens":3,"cost":0.1}`)
}

func TestBridgeUsageCostWriter_RepeatedTerminalCosts(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	w := &bridgeUsageCostWriter{ResponseWriter: c.Writer, stream: true}
	first := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"cost\":999}}\n\n"
	last := "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"cost\":888}}\n\n"
	_, err := w.WriteString(first + last + "data: [DONE]\n\n")
	require.NoError(t, err)
	_, err = w.finish(func() *CostBreakdown { return &CostBreakdown{TotalCost: 0.1} })
	require.NoError(t, err)
	require.Equal(t, strings.Replace(first, `,"cost":999`, "", 1)+strings.Replace(last, "888", "0.1", 1)+"data: [DONE]\n\n", rec.Body.String())
}

func TestBridgeUsageCostWriter_MixedTerminalReleasesCandidateOutput(t *testing.T) {
	for _, delta := range []string{`{"content":"answer"}`, `{"tool_calls":[{"index":0,"function":{"name":"get_weather","arguments":"{}"}}]}`} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		attempt := newCandidateChatAttempt(context.Background(), c.Writer, true, time.Minute)
		defer attempt.close()
		w := &bridgeUsageCostWriter{ResponseWriter: attempt, stream: true}
		payload := `{"choices":[{"index":0,"delta":` + delta + `,"finish_reason":"stop"}],"usage":{"prompt_tokens":3}}`
		_, err := w.WriteString("data: " + payload + "\n\ndata: [DONE]\n\n")
		require.NoError(t, err)
		w.Flush()
		require.True(t, attempt.started, "mixed answer must commit useful output before settlement")
		require.Contains(t, rec.Body.String(), delta)
		require.NotContains(t, rec.Body.String(), `"usage"`)
		_, err = w.finish(func() *CostBreakdown { return &CostBreakdown{TotalCost: 0.1} })
		require.NoError(t, err)
		require.Equal(t, 1, strings.Count(rec.Body.String(), delta), "do not replay tool/content deltas")
		require.Contains(t, rec.Body.String(), `"choices":[],"usage":{"prompt_tokens":3,"cost":0.1}`)
		require.True(t, strings.HasSuffix(rec.Body.String(), "data: [DONE]\n\n"))
	}
}
