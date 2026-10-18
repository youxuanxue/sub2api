//go:build unit

package service

import (
	"bytes"
	"context"
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
	require.True(t, HasOpsClientContentFiltered(c))
}
