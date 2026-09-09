//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestCandidateChatWaitIgnoresHeadersHeartbeatAndEmptyDeltas(t *testing.T) {
	for _, input := range []string{"", ": heartbeat\n\n", "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"\"}}]}\n\n", "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1}}\n\n"} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		a := newCandidateChatAttempt(context.Background(), c.Writer, true, 20*time.Millisecond)
		a.Header().Set("X-Uncommitted", "discard")
		a.WriteHeader(http.StatusOK)
		a.WriteHeaderNow()
		_, err := a.WriteString(input)
		require.NoError(t, err)
		a.Flush()
		select {
		case <-a.ctx.Done():
		case <-time.After(time.Second):
			t.Fatal("attempt was not canceled")
		}
		require.ErrorIs(t, context.Cause(a.ctx), errCandidateChatFirstOutput)
		require.Empty(t, rec.Body.String())
		require.Empty(t, rec.Header().Get("X-Uncommitted"))
		a.close()
	}
}

func TestCandidateChatBudgetAndReplayBoundaries(t *testing.T) {
	a := globalCandidateAccount(1, 1, 10)
	a.Platform, a.ChannelType = PlatformNewAPI, 1
	attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions)
	r, _, key := globalCandidateFixture([]Group{grp(10, PlatformNewAPI, 1, false)}, []Account{a})
	ctx, state := prepareGlobalCandidate(t, r, key)
	ctx = withProtocolExecutionPlan(ctx, *state.current.plan)
	body := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"stream":true}`)
	for _, blocked := range []string{
		`{"previous_response_id":"resp_old"}`,
		`{"tools":[{"type":"web_search"}]}`,
		`{"modalities":["text","audio"]}`,
	} {
		require.False(t, candidateChatReplayable(state, &a, ctx, []byte(blocked)))
	}
	state.continuationAccountID = a.ID
	require.False(t, candidateChatReplayable(state, &a, ctx, body))
	state.continuationAccountID = 0
	require.True(t, candidateChatReplayable(state, &a, ctx, body))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body))).WithContext(ctx)
	for _, exhausted := range []bool{false, true} {
		state.chatAttempts = 0
		state.chatDeadline = time.Now().Add(-time.Second)
		if exhausted {
			state.chatAttempts = candidateChatMaxAttempts
			state.chatDeadline = time.Now().Add(time.Minute)
		}
		_, finish, err := r.candidateOpenAI.beginCandidateChatAttempt(ctx, c, &a, body)
		require.Nil(t, finish)
		var failure *UpstreamFailoverError
		require.ErrorAs(t, err, &failure)
		require.False(t, failure.ShouldRetryNextAccount())
		require.False(t, candidateFailureAttributable(err))
	}
}

func TestCandidateChatUsefulOutputStopsTimerAndPreservesBytes(t *testing.T) {
	for _, delta := range []string{`{"content":"hello"}`, `{"reasoning_content":"thinking"}`, `{"tool_calls":[{"index":0,"function":{"name":"lookup","arguments":"{}"}}]}`} {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		parent, cancel := context.WithCancel(context.Background())
		a := newCandidateChatAttempt(parent, c.Writer, true, time.Second)
		heartbeat := ": waiting\n\n"
		content := "data: {\"choices\":[{\"delta\":" + delta + "}]}\n\n"
		_, err := a.WriteString(heartbeat)
		require.NoError(t, err)
		_, err = a.WriteString(content)
		require.NoError(t, err)
		a.abortBeforeOutput(errCandidateChatFirstOutput)
		cancel()
		_, err = a.WriteString("data: [DONE]\n\n")
		require.NoError(t, err)
		require.NoError(t, a.ctx.Err())
		require.True(t, a.terminal)
		require.Equal(t, heartbeat+content+"data: [DONE]\n\n", rec.Body.String())
		a.close()
	}
}

func TestCandidateChatUsesActualAdaptorOutputFormat(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	a := newCandidateChatAttempt(context.Background(), c.Writer, false, time.Second)
	defer a.close()
	a.Header().Set("Content-Type", "text/event-stream")
	_, err := a.WriteString("data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\n\n")
	require.NoError(t, err)
	a.abortBeforeOutput(errCandidateChatFirstOutput)
	require.NoError(t, a.ctx.Err())
	require.True(t, a.started)
	require.True(t, a.stream)
}

func TestCandidateChatCancellationAndPendingBound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	a := newCandidateChatAttempt(ctx, c.Writer, true, time.Second)
	cancel()
	select {
	case <-a.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("client cancellation was not propagated")
	}
	require.ErrorIs(t, context.Cause(a.ctx), context.Canceled)
	a.close()
	a = newCandidateChatAttempt(context.Background(), c.Writer, true, time.Second)
	_, err := a.WriteString(strings.Repeat(":", candidateChatPendingLimit+1))
	require.ErrorIs(t, err, errCandidateChatPendingLimit)
	require.Empty(t, rec.Body.String())
	a.close()
	// A complete large JSON response is not a pre-output keepalive flood.
	a = newCandidateChatAttempt(context.Background(), c.Writer, false, time.Second)
	body := `{"choices":[{"message":{"content":"` + strings.Repeat("x", candidateChatPendingLimit) + `"}}]}`
	_, err = a.WriteString(body)
	require.NoError(t, err)
	require.Equal(t, body, rec.Body.String())
	a.close()
}

func TestCandidateChatAttemptCancelsRealHTTPBeforeHeadersOrAfterEmpty200(t *testing.T) {
	for _, headers := range []bool{false, true} {
		canceled := make(chan struct{})
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if headers {
				w.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(w, ": waiting\n\n")
				w.(http.Flusher).Flush()
			}
			<-r.Context().Done()
			close(canceled)
		}))
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		a := newCandidateChatAttempt(context.Background(), c.Writer, true, 100*time.Millisecond)
		req, err := http.NewRequestWithContext(a.ctx, http.MethodGet, server.URL, nil)
		require.NoError(t, err)
		resp, err := server.Client().Do(req)
		if headers {
			require.NoError(t, err)
			_, err = io.Copy(a, resp.Body)
			_ = resp.Body.Close()
		}
		require.Error(t, err)
		require.ErrorIs(t, context.Cause(a.ctx), errCandidateChatFirstOutput)
		require.Empty(t, rec.Body.String())
		select {
		case <-canceled:
		case <-time.After(time.Second):
			t.Fatal("upstream was not canceled")
		}
		a.close()
		server.Close()
	}
}

func TestCandidateChatNonStreamingExtendedBudget(t *testing.T) {
	a := globalCandidateAccount(1, 1, 10)
	a.Platform, a.ChannelType = PlatformNewAPI, 1
	attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions)
	r, _, key := globalCandidateFixture([]Group{grp(10, PlatformNewAPI, 1, false)}, []Account{a})

	bodyNonStream := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	bodyStream := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	// Default config (NewAPIChatFirstOutputTimeout=0/60, ResponseHeaderTimeout=600):
	// Non-streaming should use ResponseHeaderTimeout budget (3 * 600s = 1800s deadline),
	// streaming should use 1m default budget (3 * 60s = 180s deadline).
	cfg := &config.Config{}
	cfg.Gateway.NewAPIChatFirstOutputTimeout = 60
	cfg.Gateway.ResponseHeaderTimeout = 600
	r.candidateOpenAI.cfg = cfg

	// Non-streaming test
	ctxNS, stateNS := prepareGlobalCandidate(t, r, key)
	ctxNS = withProtocolExecutionPlan(ctxNS, *stateNS.current.plan)
	cNS, _ := gin.CreateTestContext(httptest.NewRecorder())
	cNS.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(bodyNonStream))).WithContext(ctxNS)

	_, finishNS, errNS := r.candidateOpenAI.beginCandidateChatAttempt(ctxNS, cNS, &a, bodyNonStream)
	require.NoError(t, errNS)
	require.NotNil(t, finishNS)
	require.True(t, stateNS.chatDeadline.After(time.Now().Add(25*time.Minute)), "non-streaming attempt should receive 3 * 10m budget")
	_, _ = finishNS(nil, nil)

	// Streaming test
	ctxS, stateS := prepareGlobalCandidate(t, r, key)
	ctxS = withProtocolExecutionPlan(ctxS, *stateS.current.plan)
	cS, _ := gin.CreateTestContext(httptest.NewRecorder())
	cS.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(bodyStream))).WithContext(ctxS)

	_, finishS, errS := r.candidateOpenAI.beginCandidateChatAttempt(ctxS, cS, &a, bodyStream)
	require.NoError(t, errS)
	require.NotNil(t, finishS)
	require.True(t, stateS.chatDeadline.Before(time.Now().Add(5*time.Minute)), "streaming attempt should receive 3 * 1m default budget")
	_, _ = finishS(nil, nil)
}
