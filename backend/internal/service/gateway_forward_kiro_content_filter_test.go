//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestForwardAsChatCompletions_KiroContentFilteredReturns400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))

	upstream := &kiroFakeUpstream{body: kiroContentFilteredEventStream()}
	svc := &GatewayService{
		cfg:          &config.Config{},
		httpUpstream: upstream,
		kiroGateway:  NewKiroGatewayService(upstream, nil, nil),
	}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, newKiroAccountForTest(), body, nil)
	require.Nil(t, result)
	var contentFilteredErr *KiroContentFilteredError
	require.ErrorAs(t, err, &contentFilteredErr)
	var failoverErr *UpstreamFailoverError
	require.NotErrorAs(t, err, &failoverErr)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "content_filter_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, KiroContentFilteredClientMessage(), gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
	require.Equal(t, KiroContentFilteredOutcome, rec.Header().Get(KiroOutcomeHeader))
}

func newKiroMirrorStubForContentFilterTest() *Account {
	return &Account{
		ID:          66,
		Name:        "kiro-us6",
		Platform:    PlatformAnthropic,
		Type:        AccountTypeAPIKey,
		Concurrency: 1,
		Credentials: map[string]any{
			"api_key":         "relay-key",
			"base_url":        "https://api-us6.tokenkey.dev",
			"mirror_platform": PlatformKiro,
		},
	}
}

func newKiroContentFilteredRelayResponse() *http.Response {
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	header.Set(KiroOutcomeHeader, KiroContentFilteredOutcome)
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"edge wording must not be parsed"}}`))),
	}
}

func TestForwardAsChatCompletions_KiroMirrorContentFilteredReturns400WithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-sonnet-4-5","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: newKiroContentFilteredRelayResponse()}
	svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}

	result, err := svc.ForwardAsChatCompletions(context.Background(), c, newKiroMirrorStubForContentFilterTest(), body, nil)
	require.Nil(t, result)
	var contentFilteredErr *KiroContentFilteredError
	require.True(t, errors.As(err, &contentFilteredErr))
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Len(t, upstream.requests, 1)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "content_filter_error", gjson.GetBytes(rec.Body.Bytes(), "error.type").String())
	require.Equal(t, KiroContentFilteredClientMessage(), gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
}

func TestForwardAsResponses_KiroMirrorContentFilteredReturns400WithoutFailover(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := []byte(`{"model":"claude-sonnet-4-5","input":"hi","stream":false}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", bytes.NewReader(body))
	upstream := &httpUpstreamRecorder{resp: newKiroContentFilteredRelayResponse()}
	svc := &GatewayService{cfg: &config.Config{}, httpUpstream: upstream}

	result, err := svc.ForwardAsResponses(context.Background(), c, newKiroMirrorStubForContentFilterTest(), body, nil)
	require.Nil(t, result)
	var contentFilteredErr *KiroContentFilteredError
	require.True(t, errors.As(err, &contentFilteredErr))
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr))
	require.Len(t, upstream.requests, 1)
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Equal(t, "content_filter", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
	require.Equal(t, KiroContentFilteredClientMessage(), gjson.GetBytes(rec.Body.Bytes(), "error.message").String())
}
