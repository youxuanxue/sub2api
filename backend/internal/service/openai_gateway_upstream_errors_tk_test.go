package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestTkShouldPassthroughOpenAINativeClientError(t *testing.T) {
	t.Parallel()

	require.True(t, tkShouldPassthroughOpenAINativeClientError(
		http.StatusUnprocessableEntity,
		"Invalid schema for field messages",
		[]byte(`{"error":{"message":"Invalid schema for field messages"}}`),
	))
	require.False(t, tkShouldPassthroughOpenAINativeClientError(
		http.StatusNotFound,
		"Unknown request URL",
		[]byte(`{"error":{"message":"Unknown request URL"}}`),
	))
	require.True(t, tkShouldPassthroughOpenAINativeClientError(
		http.StatusNotFound,
		"model not found",
		[]byte(`{"error":{"code":"model_not_found","message":"model not found"}}`),
	))
	require.False(t, tkShouldPassthroughOpenAINativeClientError(
		http.StatusBadGateway,
		"temporary outage",
		nil,
	))
}

func TestHandleErrorResponse_TkPassthrough422AfterNoFailover(t *testing.T) {
	c, rec := newOpenAIUpstreamErrorTestContext(t)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	body := `{"error":{"message":"Invalid schema for field messages","type":"invalid_request_error"}}`
	_, err := svc.handleErrorResponse(
		context.Background(),
		newOpenAIUpstreamErrorResponse(http.StatusUnprocessableEntity, body),
		c, newOpenAIUpstreamErrorTestAccount(), nil,
	)

	require.Error(t, err)
	require.Equal(t, http.StatusUnprocessableEntity, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "Invalid schema for field messages", gjson.Get(rec.Body.String(), "error.message").String())
}

func TestHandleErrorResponse_TkPassthroughModelNotFound404AfterNoFailover(t *testing.T) {
	c, rec := newOpenAIUpstreamErrorTestContext(t)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	body := `{"error":{"code":"model_not_found","message":"The model 'gpt-missing' does not exist","type":"invalid_request_error"}}`
	_, err := svc.handleErrorResponse(
		context.Background(),
		newOpenAIUpstreamErrorResponse(http.StatusNotFound, body),
		c, newOpenAIUpstreamErrorTestAccount(), nil,
	)

	require.Error(t, err)
	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Equal(t, "invalid_request_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Contains(t, gjson.Get(rec.Body.String(), "error.message").String(), "does not exist")
}

func TestHandleErrorResponse_TkOps404UnknownURLStays502(t *testing.T) {
	c, rec := newOpenAIUpstreamErrorTestContext(t)
	svc := &OpenAIGatewayService{cfg: &config.Config{}}

	body := `{"error":{"message":"Unknown request URL"}}`
	_, err := svc.handleErrorResponse(
		context.Background(),
		newOpenAIUpstreamErrorResponse(http.StatusNotFound, body),
		c, newOpenAIUpstreamErrorTestAccount(), nil,
	)

	require.Error(t, err)
	require.Equal(t, http.StatusBadGateway, rec.Code)
	require.Equal(t, "upstream_error", gjson.Get(rec.Body.String(), "error.type").String())
	require.Equal(t, "Upstream request failed", gjson.Get(rec.Body.String(), "error.message").String())
}
