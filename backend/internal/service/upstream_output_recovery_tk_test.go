//go:build unit

package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestResponsesMetadataBeforeOverloadAllowsFailover(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, keepalive := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough=%t/keepalive=%t", passthrough, keepalive), func(t *testing.T) {
				c, rec := newOpenAITransportErrTestContext()
				if keepalive {
					emitRecoveryHeaderKeepalive(t, c)
				}
				preamble := rec.Body.String()
				svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
				stream := strings.Join([]string{
					`data: {"type":"response.created","response":{"id":"resp_test"}}`, "",
					`data: {"type":"response.in_progress","response":{"id":"resp_test"}}`, "",
					`data: {"type":"response.metadata","metadata":{"model":"test"}}`, "",
					`data: {"type":"response.failed","response":{"id":"resp_test","status":"failed","error":{"type":"service_unavailable_error","message":"Our servers are currently overloaded. Please try again later."}}}`, "", "",
				}, "\n")
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}
				account := &Account{ID: 237, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "test", "test")
				} else {
					_, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "test", "test")
				}
				var failover *UpstreamFailoverError
				require.ErrorAs(t, err, &failover)
				require.True(t, failover.ShouldRetryNextAccount())
				require.Equal(t, http.StatusServiceUnavailable, failover.ClientStatusCode)
				require.Equal(t, keepalive, failover.SafeToFailoverAfterWrite)
				require.Equal(t, keepalive, c.Writer.Written())
				require.Equal(t, preamble, rec.Body.String(), "failed attempt metadata must not leak into the next response")
			})
		}
	}
}

type recoveryKeepaliveWriter struct {
	gin.ResponseWriter
	written chan struct{}
}

func (w *recoveryKeepaliveWriter) Write(p []byte) (int, error) {
	n, err := w.ResponseWriter.Write(p)
	select {
	case w.written <- struct{}{}:
	default:
	}
	return n, err
}
func emitRecoveryHeaderKeepalive(t *testing.T, c *gin.Context) {
	t.Helper()
	original := c.Writer
	w := &recoveryKeepaliveWriter{ResponseWriter: original, written: make(chan struct{}, 1)}
	c.Writer = w
	k := startAnthropicMessageStartKeepalive(c, time.Millisecond, "test")
	select {
	case <-w.written:
	case <-time.After(time.Second):
		k.stop()
		t.Fatal("keepalive did not write")
	}
	k.stop()
	c.Writer = original
}
func TestHeaderWaitTLSFailureAllowsOnlyKeepaliveReplay(t *testing.T) {
	for _, semantic := range []bool{false, true} {
		c, rec := newOpenAITransportErrTestContext()
		emitRecoveryHeaderKeepalive(t, c)
		emitRecoveryHeaderKeepalive(t, c)
		require.Equal(t, 1, strings.Count(rec.Body.String(), "event: message_start"), "retry must keep one client lifecycle")
		if semantic {
			_, err := c.Writer.WriteString("event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"answer\"}}\n\n")
			require.NoError(t, err)
		}
		err := (&OpenAIGatewayService{}).handleOpenAIUpstreamTransportError(context.Background(), c, &Account{ID: 240, Platform: PlatformNewAPI}, errors.New("net/http: TLS handshake timeout"), false)
		var failover *UpstreamFailoverError
		require.ErrorAs(t, err, &failover)
		require.Equal(t, !semantic, failover.SafeToFailoverAfterWrite)
		require.Equal(t, semantic, openAIStreamClientOutputStarted(c, false))
	}
}

func TestBufferedAnthropicTruncationNeverReturnsNormalCompletion(t *testing.T) {
	for _, block := range []string{`{"type":"thinking","thinking":"partial reasoning"}`, `{"type":"redacted_thinking","data":"ciphertext"}`, `{"type":"unknown_media"}`, `{"type":"text","text":""}`, `{"type":"text","text":"partial answer"}`, `{"type":"tool_use","id":"call_test","name":"lookup","input":{}}`} {
		for _, path := range []string{"gateway_chat", "native_chat", "gateway_responses", "native_responses"} {
			c, rec := newOpenAITransportErrTestContext()
			stream := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_test\",\"role\":\"assistant\",\"content\":[]}}\n\n" + "event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":" + block + "}\n\n"
			resp := newBufferedErrorUpstreamResponse(stream)
			account := bufferedAnthropicTestAccount(PlatformAnthropic)
			var err error
			switch path {
			case "native_chat":
				result, e := (&OpenAIGatewayService{}).handleCCBufferedFromNativeAnthropic(resp, c, account, "test", "test", "test", nil, time.Now())
				err = e
				require.Nil(t, result)
			case "gateway_chat":
				result, e := (&GatewayService{}).handleCCBufferedFromAnthropic(resp, c, account, "test", "test", nil, time.Now())
				err = e
				require.Nil(t, result)
			case "native_responses":
				result, e := (&OpenAIGatewayService{}).handleResponsesBufferedFromNativeAnthropic(resp, c, account, "test", "test", "test", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				err = e
				require.Nil(t, result)
			case "gateway_responses":
				result, e := (&GatewayService{}).handleResponsesBufferedStreamingResponse(resp, c, account, "test", "test", nil, time.Now(), apicompat.ResponsesClientToolMapping{})
				err = e
				require.Nil(t, result)
			}
			require.Error(t, err)
			var failover *UpstreamFailoverError
			retryable := gjson.Get(block, "type").String() == "thinking" || gjson.Get(block, "type").String() == "redacted_thinking" || (gjson.Get(block, "type").String() == "text" && gjson.Get(block, "text").String() == "")
			if retryable {
				require.ErrorAs(t, err, &failover)
			} else {
				require.NotErrorAs(t, err, &failover, "do not replay an attempt with answer/tool output")
			}
			require.NotContains(t, rec.Body.String(), `"finish_reason":"stop"`)
			require.False(t, c.Writer.Written(), "handler owns the final error or next attempt")
		}
	}
}

func TestBridgeMalformedUpstreamBodyFailsOverWithoutDisablingAccount(t *testing.T) {
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	svc, repo, blocker, incidents := newBridgePenaltyTestService()
	apiErr := newapitypes.NewOpenAIError(errors.New("invalid character '<' looking for beginning of value"), newapitypes.ErrorCodeBadResponseBody, http.StatusInternalServerError)
	err := bridgeWrapRelayErrorAfterPenalty(context.Background(), svc, c, newNewAPIBridgeAccount(), apiErr)
	var failover *UpstreamFailoverError
	require.ErrorAs(t, err, &failover)
	require.True(t, failover.ShouldRetryNextAccount())
	require.True(t, candidateFailureAttributable(err), "reuse existing short lived supplier health penalty")
	require.False(t, c.Writer.Written())
	require.Zero(t, repo.setErrorCalls)
	require.Zero(t, repo.setRateLimitedCalls)
	require.Zero(t, repo.tempCalls)
	require.Empty(t, blocker.reasons)
	require.Empty(t, incidents.reasons)
	require.True(t, tkBridgeUpstreamShouldFailoverAfterPenalty(nil, upstreamBridgeError(500, "internal error")),
		"unclassified NewAPI 500 must failover after the 500-default SSOT amendment")
	require.False(t, tkBridgeUpstreamShouldFailoverAfterPenalty(nil, newapitypes.NewOpenAIError(errors.New("invalid request"), newapitypes.ErrorCodeBadResponseBody, http.StatusBadRequest)))
}

func TestResponsesMetadataPreservesSuccessfulOrderAndCommittedFailure(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("passthrough=%t/failed=%t", passthrough, failed), func(t *testing.T) {
				c, rec := newOpenAITransportErrTestContext()
				svc := &OpenAIGatewayService{cfg: &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}}}
				terminal := `{"type":"response.completed","response":{"id":"resp_test","status":"completed","output":[]}}`
				if failed {
					terminal = `{"type":"response.failed","response":{"error":{"code":"server_is_overloaded","message":"overloaded"}}}`
				}
				stream := "data: " + `{"type":"response.created","response":{"id":"resp_test"}}` + "\n\ndata: " + `{"type":"response.metadata","metadata":{"model":"test"}}` + "\n\ndata: " + `{"type":"response.output_text.delta","delta":"answer"}` + "\n\ndata: " + terminal + "\n\n"
				resp := &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(stream))}
				account := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeOAuth}
				var err error
				if passthrough {
					_, err = svc.handleStreamingResponsePassthrough(context.Background(), resp, c, account, time.Now(), "test", "test")
				} else {
					_, err = svc.handleStreamingResponse(context.Background(), resp, c, account, time.Now(), "test", "test")
				}
				var failover *UpstreamFailoverError
				require.NotErrorAs(t, err, &failover)
				if failed {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
				body := rec.Body.String()
				require.Contains(t, body, "response.metadata")
				require.Contains(t, body, "answer")
				require.Less(t, strings.Index(body, "response.created"), strings.Index(body, "response.metadata"))
				require.Less(t, strings.Index(body, "response.metadata"), strings.Index(body, "answer"))
			})
		}
	}
}

func TestHeaderWaitCanceledRequestDoesNotFailOver(t *testing.T) {
	c, _ := newOpenAITransportErrTestContext()
	emitRecoveryHeaderKeepalive(t, c)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := (&OpenAIGatewayService{}).handleOpenAIUpstreamTransportError(ctx, c, &Account{ID: 1}, context.Canceled, false)
	require.ErrorIs(t, err, context.Canceled)
	var failover *UpstreamFailoverError
	require.NotErrorAs(t, err, &failover)
}
