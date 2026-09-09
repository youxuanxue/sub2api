package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type bridgeReplayTestCache struct {
	*candidateIdentityTestCache
	mu        sync.Mutex
	replay    map[string][]byte
	replayErr error
}

func (c *bridgeReplayTestCache) SetOpenAIWSBridgeReplay(_ context.Context, key string, raw []byte, _ time.Duration) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.replayErr != nil {
		return c.replayErr
	}
	if c.replay == nil {
		c.replay = map[string][]byte{}
	}
	c.replay[key] = append([]byte(nil), raw...)
	return nil
}

func (c *bridgeReplayTestCache) GetOpenAIWSBridgeReplay(_ context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]byte(nil), c.replay[key]...), c.replayErr
}

func TestOpenAIWSBridgeReplayIsolationAndFailedReads(t *testing.T) {
	cache := &bridgeReplayTestCache{candidateIdentityTestCache: &candidateIdentityTestCache{}}
	writer := &OpenAIGatewayService{cache: cache}
	reader := &OpenAIGatewayService{cache: cache}
	ctx := WithCandidateIdentity(context.Background(), 1, 10)
	input := []json.RawMessage{json.RawMessage(`{"role":"user","content":"private"}`)}
	require.NoError(t, writer.saveOpenAIWSBridgeReplay(ctx, 65, "response", input))
	got, err := reader.loadOpenAIWSBridgeReplay(WithCandidateIdentity(context.Background(), 1, 11), 65, "response")
	require.NoError(t, err)
	require.Equal(t, input, got)
	_, err = reader.loadOpenAIWSBridgeReplay(WithCandidateIdentity(context.Background(), 2, 10), 65, "response")
	require.ErrorIs(t, err, ErrCandidateContinuationUnavailable)
	_, err = reader.loadOpenAIWSBridgeReplay(ctx, 66, "response")
	require.ErrorIs(t, err, ErrCandidateContinuationUnavailable)
	_, err = reader.loadOpenAIWSBridgeReplay(ctx, 65, "missing")
	require.ErrorIs(t, err, ErrCandidateContinuationUnavailable)
	cache.replayErr = errors.New("redis unavailable")
	_, err = reader.loadOpenAIWSBridgeReplay(ctx, 65, "response")
	require.ErrorIs(t, err, cache.replayErr)
	cache.replayErr = nil
	oversize, err := json.Marshal(map[string]string{"content": strings.Repeat("x", OpenAIWSBridgeReplayMaxBytes)})
	require.NoError(t, err)
	require.Error(t, writer.saveOpenAIWSBridgeReplay(ctx, 65, "large", []json.RawMessage{oversize}))
	_, err = reader.loadOpenAIWSBridgeReplay(ctx, 65, "large")
	require.ErrorIs(t, err, ErrCandidateContinuationUnavailable)
}

func TestGrokWSBridgeReconnectAcrossGatewayInstances(t *testing.T) {
	gin.SetMode(gin.TestMode)
	cache := &bridgeReplayTestCache{candidateIdentityTestCache: &candidateIdentityTestCache{}}
	account := &Account{ID: 65, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1,
		Credentials: map[string]any{"api_key": "test", "base_url": "https://example.test"}}
	ctx := WithCandidateIdentity(context.Background(), 1, 10)
	previous := ""
	var observedBodies []string
	for turn := 0; turn < 3; turn++ {
		responseID := fmt.Sprintf("00000000-0000-4000-8000-%012d", turn+1)
		upstream := &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200,
			Header: http.Header{"Content-Type": []string{"text/event-stream"}},
			Body:   io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"id":"` + responseID + `","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"private-marker"}]}],"usage":{"input_tokens":10,"output_tokens":1}}}` + "\n\n"))}}
		cfg := &config.Config{}
		cfg.Gateway.OpenAIWS.Enabled = true
		svc := &OpenAIGatewayService{cfg: cfg, cache: cache, httpUpstream: upstream}
		if previous != "" {
			owner, err := svc.ResolveCandidateContinuation(ctx, &APIKey{ID: 10, UserID: 1}, []Group{{ID: 270}}, previous)
			require.NoError(t, err)
			require.Equal(t, int64(65), owner)
		}
		body := map[string]any{"type": "response.create", "model": "grok-4.6", "store": true, "input": []any{map[string]any{"role": "user", "content": "private-marker"}}}
		if previous != "" {
			body["previous_response_id"] = previous
			body["input"] = []any{map[string]any{"role": "user", "content": "repeat the earlier marker"}}
		}
		raw, err := json.Marshal(body)
		require.NoError(t, err)
		completed, proxyErr := runGrokBridgeTestConnection(t, svc, account, ctx, raw)
		require.NoError(t, proxyErr)
		require.Equal(t, responseID, gjson.GetBytes(completed, "response.id").String())
		require.Len(t, upstream.bodies, 1)
		posted := string(upstream.bodies[0])
		require.NotContains(t, posted, "previous_response_id")
		require.Contains(t, posted, "private-marker")
		if turn > 0 {
			require.GreaterOrEqual(t, len(gjson.Get(posted, "input").Array()), 2*turn+1)
		}
		observedBodies = append(observedBodies, posted)
		previous = responseID
	}
	require.Len(t, observedBodies, 3)
}

func runGrokBridgeTestConnection(t *testing.T, svc *OpenAIGatewayService, account *Account, identity context.Context, payloads ...[]byte) ([]byte, error) {
	t.Helper()
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(identity, 5*time.Second)
		defer cancel()
		_, first, err := conn.Read(ctx)
		if err != nil {
			done <- err
			return
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r.WithContext(ctx)
		groupID := int64(270)
		c.Set("api_key", &APIKey{ID: 10, UserID: 1, GroupID: &groupID})
		err = svc.ProxyResponsesWebSocketFromClient(ctx, c, conn, account, "test", first, nil)
		var closeErr *OpenAIWSClientCloseError
		if errors.As(err, &closeErr) {
			_ = conn.Close(closeErr.StatusCode(), closeErr.Reason())
		}
		done <- err
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer func() { _ = client.CloseNow() }()
	var message []byte
	for _, raw := range payloads {
		require.NoError(t, client.Write(ctx, websocket.MessageText, raw))
		_, message, err = client.Read(ctx)
		if err != nil {
			break
		}
	}
	_ = client.Close(websocket.StatusNormalClosure, "done")
	select {
	case err := <-done:
		return message, err
	case <-ctx.Done():
		t.Fatal("bridge did not terminate")
		return nil, ctx.Err()
	}
}

func TestGrokWSBridgeMissingHistoryNeverBecomesFreshRequest(t *testing.T) {
	cache := &bridgeReplayTestCache{candidateIdentityTestCache: &candidateIdentityTestCache{}}
	upstream := &httpUpstreamRecorder{}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, cache: cache, httpUpstream: upstream}
	account := &Account{ID: 65, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1}
	_, err := runGrokBridgeTestConnection(t, svc, account, WithCandidateIdentity(context.Background(), 1, 10), []byte(`{"type":"response.create","model":"grok-4.6","previous_response_id":"unknown","input":"continue"}`))
	require.ErrorIs(t, err, ErrCandidateContinuationUnavailable)
	require.Empty(t, upstream.bodies)
}

func TestGrokWSBridgeStoreFalseChainIsNotPersisted(t *testing.T) {
	cache := &bridgeReplayTestCache{candidateIdentityTestCache: &candidateIdentityTestCache{}}
	response := func(id string) *http.Response {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(`data: {"type":"response.completed","response":{"id":"` + id + `","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"private reply"}]}],"usage":{"input_tokens":1,"output_tokens":1}}}` + "\n\n"))}
	}
	upstream := &httpUpstreamRecorder{responses: []*http.Response{response("resp_private"), response("resp_child")}}
	svc := &OpenAIGatewayService{cfg: &config.Config{}, cache: cache, httpUpstream: upstream}
	account := &Account{ID: 65, Platform: PlatformGrok, Type: AccountTypeAPIKey, Concurrency: 1, Credentials: map[string]any{"base_url": "https://example.test"}}
	_, err := runGrokBridgeTestConnection(t, svc, account, WithCandidateIdentity(context.Background(), 1, 10),
		[]byte(`{"type":"response.create","model":"grok-4.6","store":false,"input":"private question"}`),
		[]byte(`{"type":"response.create","model":"grok-4.6","store":true,"previous_response_id":"resp_private","input":"continue"}`))
	require.NoError(t, err)
	require.Len(t, upstream.bodies, 2)
	require.Contains(t, string(upstream.bodies[1]), "private question")
	require.Contains(t, string(upstream.bodies[1]), "private reply")
	require.Empty(t, cache.replay)
}
