package handler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestFinalizeFirstOutputOutcome(t *testing.T) {
	for _, tc := range []struct {
		name      string
		status    int
		cancelled bool
		want      string
	}{
		{"recovered", 200, false, "recovered"},
		{"failed", 502, false, "final_failure"},
		{"client left", 200, true, "client_cancel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			if tc.cancelled {
				ctx, cancel := context.WithCancel(c.Request.Context())
				cancel()
				c.Request = c.Request.WithContext(ctx)
			}
			w := acquireOpsCaptureWriter(c.Writer)
			defer releaseOpsCaptureWriter(w)
			c.Writer = w
			c.Writer.WriteHeader(tc.status)
			event := &service.OpsUpstreamErrorEvent{Kind: "first_output_timeout", BudgetMS: 120000, BudgetReason: "default"}
			c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{event, {Kind: "request_error"}})
			service.BeginOpsFirstOutputAttempt(c)
			service.BeginOpsFirstOutputAttempt(c)
			service.BeginOpsFirstOutputAttempt(c) // Successful last attempt has no error event.
			finalizeOpsFirstOutputEvaluation(c, w)
			require.Equal(t, 3, event.AttemptCount)
			require.Equal(t, tc.want, event.TimeoutOutcome)
			require.Equal(t, int64(120000), event.BudgetMS)
		})
	}
}

func TestFinalizeFirstOutputInBandFailure(t *testing.T) {
	for _, websocket := range []bool{false, true} {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
		w := acquireOpsCaptureWriter(c.Writer)
		c.Writer = w
		event := &service.OpsUpstreamErrorEvent{Kind: "first_output_timeout"}
		c.Set(service.OpsUpstreamErrorsKey, []*service.OpsUpstreamErrorEvent{event})
		service.BeginOpsFirstOutputAttempt(c)
		if websocket {
			c.Writer.WriteHeader(http.StatusSwitchingProtocols)
			c.Set("ops_first_output_ws_failed", true)
		} else {
			c.Writer.Header().Set("Content-Type", "text/event-stream")
			_, err := c.Writer.WriteString("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"type\":\"server_error\",\"message\":\"timeout\"}}}\n\n")
			require.NoError(t, err)
			w.finalizeCapture()
		}
		finalizeOpsFirstOutputEvaluation(c, w)
		require.Equal(t, "final_failure", event.TimeoutOutcome)
		releaseOpsCaptureWriter(w)
	}
}
