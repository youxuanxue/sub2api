//go:build unit

package handler

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type recoveryFlushWriter struct {
	gin.ResponseWriter
	flushed chan struct{}
}

func (w *recoveryFlushWriter) Flush() {
	w.ResponseWriter.Flush()
	select {
	case w.flushed <- struct{}{}:
	default:
	}
}

type recoveryTLSUpstream struct {
	flushed   <-chan struct{}
	calls     []int64
	exhausted bool
}

func (u *recoveryTLSUpstream) Do(req *http.Request, _ string, id int64, _ int) (*http.Response, error) {
	u.calls = append(u.calls, id)
	if len(u.calls) == 1 || u.exhausted {
		select {
		case <-u.flushed:
			return nil, errors.New("net/http: TLS handshake timeout")
		case <-req.Context().Done():
			return nil, req.Context().Err()
		case <-time.After(3 * time.Second):
			return nil, errors.New("test keepalive did not arrive")
		}
	}
	stream := "event: message_start\ndata: " + `{"type":"message_start","message":{"id":"msg_ok","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[],"usage":{"input_tokens":3}}}` + "\n\nevent: content_block_start\ndata: " + `{"type":"content_block_start","index":0,"content_block":{"type":"text","text":"recovered"}}` + "\n\nevent: content_block_stop\ndata: " + `{"type":"content_block_stop","index":0}` + "\n\nevent: message_delta\ndata: " + `{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":1}}` + "\n\nevent: message_stop\ndata: " + `{"type":"message_stop"}` + "\n\n"
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(stream))}, nil
}
func (u *recoveryTLSUpstream) DoWithTLS(req *http.Request, proxy string, id int64, n int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxy, id, n)
}

// Exercise the real selected-account executor, header-wait writer, transport
// classifier and handler retry/exhaustion gates together on one client stream.
func TestHeaderWaitTLSRecoveryKeepsSingleMessagesLifecycle(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		t.Run(map[bool]string{false: "recovered", true: "exhausted"}[exhausted], func(t *testing.T) {
			cfg := &config.Config{RunMode: config.RunModeSimple}
			cfg.Gateway.StreamKeepaliveInterval = 1
			group := service.Group{ID: 10, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1, Hydrated: true}
			accounts := []service.Account{}
			for i := int64(1); i <= 2; i++ {
				a := service.Account{ID: i, Platform: service.PlatformNewAPI, ChannelType: 14, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Priority: int(i), Concurrency: 10, GroupIDs: []int64{10}, Credentials: map[string]any{"api_key": "fixture", "base_url": "https://fixture.example", "model_mapping": map[string]any{"claude-sonnet-4-6": "claude-sonnet-4-6"}}}
				attachHandlerTestProtocolCapability(t, &a, protocolrouter.ProtocolMessages)
				accounts = append(accounts, a)
			}
			repo := &candidateNativeRepo{accounts: accounts}
			flushed := make(chan struct{}, 1)
			transport := &recoveryTLSUpstream{flushed: flushed, exhausted: exhausted}
			gateway := service.NewGatewayService(repo, nil, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, nil, transport, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
			openai := service.NewOpenAIGatewayService(repo, nil, nil, nil, nil, nil, nil, cfg, nil, nil, nil, nil, nil, transport, nil, nil, nil, nil, nil, nil, nil, nil)
			api := service.NewAPIKeyService(nil, nil, nil, nil, nil, nil, cfg)
			router := service.NewProtocolRouter()
			service.ProvideTKUniversalModelsProvider(api, gateway, nil, openai, router)
			key := &service.APIKey{ID: 1, UserID: 7, Group: &group, GroupID: &group.ID, User: &service.User{ID: 7, Balance: 10}}
			body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":32,"messages":[{"role":"user","content":"hello"}],"stream":true}`)
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Writer = &recoveryFlushWriter{ResponseWriter: c.Writer, flushed: flushed}
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(string(body)))
			_, err := api.UniversalResolver().PrepareCandidateIngress(c, key, service.ShapeAnthropicMessages, "/v1/messages", "claude-sonnet-4-6", body, "")
			require.NoError(t, err)
			h := &OpenAIGatewayHandler{gatewayService: openai, nativeGatewayService: gateway, cfg: cfg}
			executors := h.candidateProtocolExecutors(c, service.ProtocolExecutors{
				MessagesIdentity: func(ctx context.Context, account *service.Account, _ protocolrouter.Plan, request protocolrouter.CanonicalRequest) (any, error) {
					return openai.ForwardAsAnthropic(ctx, c, account, request.Body(), "", "")
				},
			})
			excluded := map[int64]struct{}{}
			switches := 0
			for attempt := 0; attempt < 2; attempt++ {
				selected, err := gateway.SelectAccountWithLoadAwareness(c.Request.Context(), key.GroupID, "", "claude-sonnet-4-6", excluded, "", key.UserID)
				require.NoError(t, err)
				before := c.Writer.Size()
				_, err = service.ExecuteSelectedProtocol(c.Request.Context(), router, selected, selected.Account, gateway.ValidateProtocolEndpoint, gateway.LoadProtocolExecutionAccount, executors)
				selected.ReleaseFunc()
				if err == nil {
					require.False(t, exhausted)
					break
				}
				var failover *service.UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.True(t, failover.SafeToFailoverAfterWrite)
				require.True(t, gatewayForwardMayFailover(c, before, failover))
				if openAIFirstOutputFailoverExhausted(failover, &switches) {
					require.True(t, exhausted)
					h.handleAnthropicFailoverExhausted(c, failover, true)
					break
				}
				excluded[selected.Account.ID] = struct{}{}
			}
			require.Equal(t, []int64{1, 2}, transport.calls)
			require.Contains(t, rec.Body.String(), "event: ping")
			if exhausted {
				// Native Messages emits ping-only before upstream headers; only
				// a healthy upstream can supply message_start on this path.
				require.Zero(t, strings.Count(rec.Body.String(), "event: message_start"))
				require.Contains(t, rec.Body.String(), "event: error")
				require.NotContains(t, rec.Body.String(), "event: message_stop")
			} else {
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_start"))
				require.Contains(t, rec.Body.String(), "recovered")
				require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_stop"))
				require.NotContains(t, rec.Body.String(), "event: error")
			}
		})
	}
}
