//go:build unit

package service

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	newapitypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsOpenAICompatModelNotFound404(t *testing.T) {
	cases := []struct {
		name string
		body string
		msg  string
		want bool
	}{
		{"volcengine error.code in JSON body", `{"error":{"code":"InvalidEndpointOrModel.NotFound","message":"The model ` + "`x`" + ` does not exist or you do not have access to it."}}`, "", true},
		{"volcengine message substring", "", "The model `doubao-lite-32k-240828` does not exist or you do not have access to it.", true},
		{"code-prefixed message (as recorded by the bridge dispatch)", "", "InvalidEndpointOrModel.NotFound: The model `x` does not exist or you do not have access to it.", true},
		// DashScope / Qwen (channel_type=17) real 404 (direct probe 2026-06-10): the
		// OpenAI-standard model_not_found envelope. Matched by BOTH the prose phrase
		// and the structured code, so a vendor reword of either still classifies.
		{"dashscope model_not_found JSON body", `{"error":{"message":"The model ` + "`qwen-x`" + ` does not exist or you do not have access to it.","type":"invalid_request_error","code":"model_not_found"}}`, "", true},
		{"dashscope code-prefixed message (bridge path)", "", "model_not_found: The model `qwen-x` does not exist or you do not have access to it.", true},
		{"model_not_found structured code alone (prose reworded)", `{"error":{"code":"model_not_found","message":"whatever wording the vendor uses"}}`, "", true},
		{"genuine 5xx is NOT model-not-found", `{"error":{"message":"upstream service temporarily unavailable"}}`, "Upstream request failed", false},
		{"rate limit is NOT model-not-found", "", "Upstream rate limit exceeded, please retry later", false},
		{"empty", "", "", false},
	}
	for _, tc := range cases {
		assert.Equalf(t, tc.want, IsOpenAICompatModelNotFound404([]byte(tc.body), tc.msg), "case %q", tc.name)
	}
}

func TestTkRecordBridgeUpstreamError_RecordsRealUpstream404(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// nil-safe.
	TkRecordBridgeUpstreamError(nil, http.StatusNotFound, nil)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	TkRecordBridgeUpstreamError(c, http.StatusNotFound, nil) // nil err is a no-op
	_, ok := c.Get(OpsUpstreamStatusCodeKey)
	require.False(t, ok, "nil err must not touch the ops context")

	apiErr := newapitypes.NewErrorWithStatusCode(
		errors.New("The model `doubao-lite-32k-240828` does not exist or you do not have access to it."),
		newapitypes.ErrorCode("InvalidEndpointOrModel.NotFound"),
		http.StatusNotFound,
	)
	TkRecordBridgeUpstreamError(c, apiErr.StatusCode, apiErr)

	status, ok := c.Get(OpsUpstreamStatusCodeKey)
	require.True(t, ok)
	require.Equal(t, http.StatusNotFound, status)

	msg, ok := c.Get(OpsUpstreamErrorMessageKey)
	require.True(t, ok)
	// The code is prefixed into the message so the ops classifier (which reads only
	// the single-field message key) can see the InvalidEndpointOrModel.NotFound signal.
	require.True(t, IsOpenAICompatModelNotFound404(nil, msg.(string)),
		"recorded message must be recognized as a model-not-found")
}
