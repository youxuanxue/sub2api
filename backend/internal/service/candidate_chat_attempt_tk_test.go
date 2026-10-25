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
		a := newCandidateChatAttempt(context.Background(), c.Writer, true, time.Second)
		heartbeat := ": waiting\n\n"
		content := "data: {\"choices\":[{\"delta\":" + delta + "}]}\n\n"
		_, err := a.WriteString(heartbeat)
		require.NoError(t, err)
		_, err = a.WriteString(content)
		require.NoError(t, err)
		a.abortBeforeOutput(errCandidateChatFirstOutput)
		_, err = a.WriteString("data: [DONE]\n\n")
		require.NoError(t, err)
		require.NoError(t, a.ctx.Err(), "first-output timeout must not abort after useful output")
		require.True(t, a.terminal)
		require.Equal(t, heartbeat+content+"data: [DONE]\n\n", rec.Body.String())
		a.close()
	}
}

func TestCandidateChatClientCancelStopsAfterUsefulOutput(t *testing.T) {
	canceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, ": waiting\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(canceled)
	}))
	defer server.Close()

	parent, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	a := newCandidateChatAttempt(parent, c.Writer, true, time.Minute)
	defer a.close()

	_, err := a.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
	require.NoError(t, err)
	require.True(t, a.started, "useful output must commit the attempt")

	req, err := http.NewRequestWithContext(a.ctx, http.MethodGet, server.URL, nil)
	require.NoError(t, err)
	resp, err := server.Client().Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()

	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = io.Copy(io.Discard, resp.Body)
	}()

	cancel()
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("upstream was not canceled after client disconnect mid-stream")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("response body did not unblock after upstream cancel")
	}
	require.ErrorIs(t, context.Cause(a.ctx), context.Canceled)
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

func TestCandidateChatStreamVersusNonstreamBudgets(t *testing.T) {
	a := globalCandidateAccount(1, 1, 10)
	a.Platform, a.ChannelType = PlatformNewAPI, 1
	attachTestProtocolCapability(&a, protocolrouter.ProtocolChatCompletions)
	r, _, key := globalCandidateFixture([]Group{grp(10, PlatformNewAPI, 1, false)}, []Account{a})

	bodyNonStream := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"stream":false}`)
	bodyStream := []byte(`{"model":"gpt-5.4","messages":[{"role":"user","content":"hi"}],"stream":true}`)

	// Zero config → stream 120s × 3 = 6m deadline; non-stream 600s × 3 = 30m deadline.
	// Explicit ResponseHeaderTimeout must NOT bleed into either budget.
	cfg := &config.Config{}
	cfg.Gateway.ResponseHeaderTimeout = 600
	r.candidateOpenAI.cfg = cfg

	ctxNS, stateNS := prepareGlobalCandidate(t, r, key)
	ctxNS = withProtocolExecutionPlan(ctxNS, *stateNS.current.plan)
	cNS, _ := gin.CreateTestContext(httptest.NewRecorder())
	cNS.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(bodyNonStream))).WithContext(ctxNS)

	_, finishNS, errNS := r.candidateOpenAI.beginCandidateChatAttempt(ctxNS, cNS, &a, bodyNonStream)
	require.NoError(t, errNS)
	require.NotNil(t, finishNS)
	require.True(t, stateNS.chatDeadline.After(time.Now().Add(29*time.Minute)), "non-stream default is 3×600s")
	require.True(t, stateNS.chatDeadline.Before(time.Now().Add(31*time.Minute)), "non-stream must not use ResponseHeaderTimeout")
	_, _ = finishNS(nil, nil)

	ctxS, stateS := prepareGlobalCandidate(t, r, key)
	ctxS = withProtocolExecutionPlan(ctxS, *stateS.current.plan)
	cS, _ := gin.CreateTestContext(httptest.NewRecorder())
	cS.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(bodyStream))).WithContext(ctxS)

	_, finishS, errS := r.candidateOpenAI.beginCandidateChatAttempt(ctxS, cS, &a, bodyStream)
	require.NoError(t, errS)
	require.NotNil(t, finishS)
	require.True(t, stateS.chatDeadline.Before(time.Now().Add(370*time.Second)), "stream default is 3×120s")
	require.True(t, stateS.chatDeadline.After(time.Now().Add(350*time.Second)), "stream deadline should be near 360s")
	_, _ = finishS(nil, nil)
}

func TestNewAPIChatFirstOutputTimeoutResolver(t *testing.T) {
	require.Equal(t, 120*time.Second, newAPIChatFirstOutputTimeoutWithEffort(nil, true, false))
	require.Equal(t, 600*time.Second, newAPIChatFirstOutputTimeoutWithEffort(nil, false, false))

	cfg := &config.Config{}
	cfg.Gateway.NewAPIChatFirstOutputTimeout = 7
	cfg.Gateway.NewAPIChatNonstreamFirstOutputTimeout = 120
	require.Equal(t, 7*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, true, false))
	require.Equal(t, 120*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, false, false))
	require.Equal(t, 600*time.Second, newAPIChatFirstOutputTimeoutWithEffort(nil, true, true))
	cfg.Gateway.StreamFirstOutputTimeoutSeconds = 45
	cfg.Gateway.NewAPIChatFirstOutputTimeout = 0
	require.Equal(t, 45*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, true, false))
}

// NewAPI high-effort streaming gets its own budget so domestic models are not
// stuck on the 600s long-thinking SSOT. Covers both directions: unset must keep
// inheriting, and set must not leak into any neighboring budget.
func TestNewAPIChatHighEffortFirstOutputOverride(t *testing.T) {
	// Unset (zero) keeps the historical inherit from the stream high-effort SSOT.
	require.Equal(t, 600*time.Second, newAPIChatFirstOutputTimeoutWithEffort(nil, true, true))

	cfg := &config.Config{}
	require.Equal(t, 600*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, true, true))

	// Set: high effort uses the NewAPI override instead of 600s.
	cfg.Gateway.NewAPIChatHighEffortFirstOutputTimeout = 30
	require.Equal(t, 30*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, true, true))

	// It must not bleed into standard-effort streaming...
	require.Equal(t, 120*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, true, false))
	cfg.Gateway.NewAPIChatFirstOutputTimeout = 20
	require.Equal(t, 20*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, true, false))
	// ...nor swap with it: high effort keeps its own value, not the 20s one.
	require.Equal(t, 30*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, true, true))

	// ...nor into non-stream, which owns a separate whole-body budget.
	require.Equal(t, 600*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, false, true))
	require.Equal(t, 600*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, false, false))

	// ...nor into the cross-platform stream SSOT used by anthropic/gemini/kiro,
	// which is what keeps gpt/claude families on their long-thinking budget.
	require.Equal(t, 600*time.Second, resolveStreamFirstOutputTimeout(cfg, true))

	// The explicit stream high-effort SSOT still wins where it applies.
	cfg.Gateway.StreamFirstOutputHighEffortTimeoutSeconds = 300
	require.Equal(t, 300*time.Second, resolveStreamFirstOutputTimeout(cfg, true))
	// NewAPI override still takes precedence for NewAPI chat.
	require.Equal(t, 30*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, true, true))
	// Clearing the NewAPI override falls back to that SSOT, not to 600s.
	cfg.Gateway.NewAPIChatHighEffortFirstOutputTimeout = 0
	require.Equal(t, 300*time.Second, newAPIChatFirstOutputTimeoutWithEffort(cfg, true, true))
}
