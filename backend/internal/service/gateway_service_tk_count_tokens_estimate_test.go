//go:build unit

package service

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestEstimateAnthropicCountTokensInput_CountsMessagesAndSystem(t *testing.T) {
	t.Parallel()

	body := []byte(`{
		"model":"claude-sonnet-4-6",
		"system":[{"type":"text","text":"You are helpful."}],
		"messages":[{"role":"user","content":"hello world"}]
	}`)
	got := estimateAnthropicCountTokensInput(body)
	require.Greater(t, got, 0)
}

func TestEstimateAnthropicCountTokensInput_EmptyBodyReturnsZero(t *testing.T) {
	t.Parallel()
	require.Equal(t, 0, estimateAnthropicCountTokensInput(nil))
}

func TestForwardCountTokens_AntigravityReturnsLocalEstimate(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", nil)

	svc := &GatewayService{}
	account := &Account{ID: 1, Platform: PlatformAntigravity, Type: AccountTypeOAuth}
	parsed := &ParsedRequest{
		Model: "claude-sonnet-4-6",
		Body:  mustNewRequestBodyRef([]byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hello"}]}`)),
	}

	err := svc.ForwardCountTokens(c.Request.Context(), c, account, parsed)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"input_tokens"`)
}

func mustNewRequestBodyRef(body []byte) *RequestBodyRef {
	ref := NewRequestBodyRef(body)
	if ref == nil {
		panic("NewRequestBodyRef returned nil")
	}
	return ref
}
