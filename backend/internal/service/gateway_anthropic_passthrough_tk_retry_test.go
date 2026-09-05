//go:build unit

package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTkMaybeRetryAnthropicPassthrough400_Non400Passthrough(t *testing.T) {
	svc := &GatewayService{}
	resp := &http.Response{StatusCode: http.StatusTooManyRequests, Body: io.NopCloser(strings.NewReader(`rate`))}
	out, retried, err := svc.tkMaybeRetryAnthropicPassthrough400(
		context.Background(), nil, &Account{ID: 1, Platform: PlatformAnthropic},
		&anthropicPassthroughForwardInput{Body: []byte(`{}`)},
		resp, nil, "", "", anthropicPassthroughAuthAPIKey, time.Now(),
	)
	require.NoError(t, err)
	require.False(t, retried)
	require.Same(t, resp, out)
}

func TestTkMaybeRetryAnthropicPassthrough400_NoRectifierRestoresBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	svc := &GatewayService{}
	account := &Account{ID: 82, Platform: PlatformAnthropic}
	origBody := []byte(`{"model":"claude-sonnet-4-5","messages":[]}`)
	errJSON := `{"type":"error","error":{"type":"invalid_request_error","message":"unrelated validation failure"}}`
	resp := &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(errJSON)),
	}
	input := &anthropicPassthroughForwardInput{Body: origBody, RequestModel: "claude-sonnet-4-5"}

	out, retried, err := svc.tkMaybeRetryAnthropicPassthrough400(
		context.Background(), nil, account, input, resp, &http.Request{URL: mustParseURL("https://api.anthropic.com/v1/messages")},
		"", "token", anthropicPassthroughAuthAPIKey, time.Now(),
	)
	require.NoError(t, err)
	require.False(t, retried, "unrectifiable 400 must not break the attempt loop")
	require.NotNil(t, out)
	restored, readErr := io.ReadAll(out.Body)
	require.NoError(t, readErr)
	require.JSONEq(t, errJSON, string(restored))
	require.True(t, bytes.Equal(origBody, input.Body))
}

func mustParseURL(raw string) *url.URL {
	u, err := url.ParseRequestURI(raw)
	if err != nil {
		panic(err)
	}
	return u
}
