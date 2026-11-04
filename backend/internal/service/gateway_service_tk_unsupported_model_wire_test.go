//go:build unit

package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// Official wire shapes: docs/approved/unsupported-model-404-ssot.md §Official shapes.
func TestTkWriteUnsupportedModel_OfficialShapePerWire(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		wire TkErrorWire
		want string
	}{
		{TkErrorWireOpenAI, `{"error":{"code":"model_not_found","message":"Unsupported model: does-not-exist","param":null,"type":"invalid_request_error"}}`},
		{TkErrorWireAnthropic, `{"error":{"message":"Unsupported model: does-not-exist","type":"not_found_error"},"type":"error"}`},
		{TkErrorWireGemini, `{"error":{"code":404,"message":"Unsupported model: does-not-exist","status":"NOT_FOUND"}}`},
	} {
		t.Run(string(tc.wire), func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			TkWriteUnsupportedModel(c, tc.wire, "does-not-exist")
			require.Equal(t, http.StatusNotFound, w.Code)
			require.JSONEq(t, tc.want, w.Body.String())
		})
	}
}

func TestTkUnsupportedModelWireFields_OnlyWhenMarked(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	typ, code := TkUnsupportedModelWireFields(c, TkErrorWireAnthropic, http.StatusNotFound, "invalid_request_error", "")
	require.Equal(t, "invalid_request_error", typ)
	require.Empty(t, code)

	status, typ, msg := TkUnsupportedModelSelectionResponse(c, "x")
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "invalid_request_error", typ)
	require.Equal(t, "Unsupported model: x", msg)
	typ, code = TkUnsupportedModelWireFields(c, TkErrorWireAnthropic, status, typ, "")
	require.Equal(t, "not_found_error", typ)
	require.Empty(t, code)
	typ, code = TkUnsupportedModelWireFields(c, TkErrorWireOpenAI, status, "invalid_request_error", "")
	require.Equal(t, "invalid_request_error", typ)
	require.Equal(t, "model_not_found", code)
	// A different status on the same request is never rewritten.
	typ, _ = TkUnsupportedModelWireFields(c, TkErrorWireAnthropic, http.StatusTooManyRequests, "api_error", "")
	require.Equal(t, "api_error", typ)
}

func TestTkErrorWireForPath(t *testing.T) {
	require.Equal(t, TkErrorWireGemini, TkErrorWireForPath("/v1beta/models/x:generateContent"))
	require.Equal(t, TkErrorWireGemini, TkErrorWireForPath("/antigravity/v1beta/models/x"))
	require.Equal(t, TkErrorWireAnthropic, TkErrorWireForPath("/v1/messages"))
	require.Equal(t, TkErrorWireAnthropic, TkErrorWireForPath("/antigravity/v1/messages/count_tokens"))
	require.Equal(t, TkErrorWireOpenAI, TkErrorWireForPath("/v1/chat/completions"))
	require.Equal(t, TkErrorWireOpenAI, TkErrorWireForPath("/v1/responses"))
}

func TestTkIsGatewayUnsupportedModel404(t *testing.T) {
	for _, wire := range []TkErrorWire{TkErrorWireOpenAI, TkErrorWireAnthropic, TkErrorWireGemini} {
		body, err := json.Marshal(TkUnsupportedModelErrorFor(wire, "m").Body())
		require.NoError(t, err)
		require.True(t, TkIsGatewayUnsupportedModel404(http.StatusNotFound, body), wire)
		require.False(t, TkIsGatewayUnsupportedModel404(http.StatusBadRequest, body), wire)
	}
	require.False(t, TkIsGatewayUnsupportedModel404(http.StatusNotFound, []byte(`{"error":{"message":"Unknown URL"}}`)))
}
