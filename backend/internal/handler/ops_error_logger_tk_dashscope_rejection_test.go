package handler

import (
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestClassifyOpsDashScopeRequestRejections(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name    string
		status  int
		message string
		body    string
		client  bool
	}{
		{name: "embedding batch exceeds provider limit", status: 400, message: "InvalidParameter: <400> InternalError.Algo.InvalidParameter: Value error, batch size is invalid, it should not be larger than 10.: input.contents", client: true},
		{name: "content inspection rejection", status: 400, message: "data_inspection_failed: Output data may contain inappropriate content.", client: true},
		{name: "structured content inspection rejection", status: 400, body: `{"error":{"code":"data_inspection_failed","message":"Output data may contain inappropriate content."}}`, client: true},
		{name: "unknown invalid parameter can be adapter fault", status: 400, message: "InvalidParameter: Internal configuration is invalid"},
		{name: "inspection service failure", status: 400, message: "data_inspection_failed: Inspection service unavailable"},
		{name: "provider 503 remains visible", status: 503, message: "InvalidParameter: batch size is invalid, it should not be larger than 10."},
		{name: "account health wins", status: 400, message: "InvalidParameter: batch size is invalid, it should not be larger than 10.; credit balance exhausted"},
		{name: "echoed input is not an error signal", status: 400, body: `{"error":{"message":"Internal service failed"},"input":"data_inspection_failed: Output data may contain inappropriate content."}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			service.SetOpsUpstreamError(c, tc.status, tc.message, "")
			c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{{UpstreamStatusCode: tc.status, Message: tc.message, Detail: tc.body}})
			phase, _, owner, source := classifyOpsErrorLog(c, "api_error", "Upstream request failed", "", tc.status)
			if tc.client {
				require.Equal(t, "request", phase)
				require.Equal(t, "client", owner)
				require.Equal(t, "client_request", source)
			} else {
				require.Equal(t, "upstream", phase)
				require.Equal(t, "provider", owner)
			}
		})
	}
}
