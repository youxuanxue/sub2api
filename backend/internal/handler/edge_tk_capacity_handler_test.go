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
	sum       int64
	err       error
	lastGroup string
}

func (s *capacityReaderStub) SumConcurrencyAnthropicByGroup(_ context.Context, groupName string) (int64, error) {
	s.lastGroup = groupName
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
	// Surface-C: capacity counts only the anthropic-default scheduling pool.
	require.Equal(t, "anthropic-default", stub.lastGroup)
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
