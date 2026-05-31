//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type capacityReaderStub struct {
	sum    int64
	err    error
	called bool
}

func (s *capacityReaderStub) SumConcurrencyAnthropic(_ context.Context) (int64, error) {
	s.called = true
	return s.sum, s.err
}

func performCapacityRequest(t *testing.T, h *EdgeCapacityHandler, query string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/edge/scheduling-capacity"+query, nil)
	h.GetSchedulingCapacity(c)
	return w
}

func TestEdgeCapacityHandler_ReturnsTotalConcurrency(t *testing.T) {
	stub := &capacityReaderStub{sum: 42}
	h := NewEdgeCapacityHandler(stub)
	w := performCapacityRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data struct {
			Platform         string `json:"platform"`
			TotalConcurrency int64  `json:"total_concurrency"`
			TS               int64  `json:"ts"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "anthropic", env.Data.Platform)
	require.Equal(t, int64(42), env.Data.TotalConcurrency)
	require.NotZero(t, env.Data.TS)
	// Regression: surface-C must read the GLOBAL Σ schedulable anthropic
	// concurrency (SumConcurrencyAnthropic), not a by-group sum. The prior
	// by-group call hardcoded "anthropic-default", a group that does not exist on
	// edges (their group is "default"), so the endpoint always returned 0 and the
	// prod mirror never converged.
	require.True(t, stub.called, "must call SumConcurrencyAnthropic (global schedulable Σ)")
}

func TestEdgeCapacityHandler_DefaultsToAnthropic(t *testing.T) {
	h := NewEdgeCapacityHandler(&capacityReaderStub{sum: 7})
	w := performCapacityRequest(t, h, "")
	require.Equal(t, http.StatusOK, w.Code)
}

func TestEdgeCapacityHandler_RejectsUnsupportedPlatform(t *testing.T) {
	h := NewEdgeCapacityHandler(&capacityReaderStub{sum: 7})
	w := performCapacityRequest(t, h, "?platform=openai")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEdgeCapacityHandler_RepoErrorIs500(t *testing.T) {
	h := NewEdgeCapacityHandler(&capacityReaderStub{err: errors.New("db down")})
	w := performCapacityRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
