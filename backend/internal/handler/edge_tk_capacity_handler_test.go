//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type capacityReaderStub struct {
	sum              int64
	platformSum      int64
	platformGroupSum int64
	err              error
	lastGroup        string
	lastPlatform     string
	lastGroupID      int64
}

func (s *capacityReaderStub) SumConcurrencyAnthropicByGroup(_ context.Context, groupName string) (int64, error) {
	s.lastGroup = groupName
	return s.sum, s.err
}

func (s *capacityReaderStub) SumConcurrencyByPlatform(_ context.Context, platform string) (int64, error) {
	s.lastPlatform = platform
	return s.platformSum, s.err
}

func (s *capacityReaderStub) SumConcurrencyByPlatformAndGroupID(_ context.Context, platform string, groupID int64) (int64, error) {
	s.lastPlatform = platform
	s.lastGroupID = groupID
	return s.platformGroupSum, s.err
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
	// Surface-C: capacity counts only the "default" scheduling pool (the group
	// every edge's anthropic accounts actually belong to).
	require.Equal(t, "default", stub.lastGroup)
}

func TestEdgeCapacityHandler_DefaultsToAnthropic(t *testing.T) {
	h := NewEdgeCapacityHandler(&capacityReaderStub{sum: 7})
	w := performCapacityRequest(t, h, "")
	require.Equal(t, http.StatusOK, w.Code)
}

func TestEdgeCapacityHandler_RejectsUnsupportedPlatform(t *testing.T) {
	h := NewEdgeCapacityHandler(&capacityReaderStub{sum: 7})
	w := performCapacityRequest(t, h, "?platform=gemini")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestEdgeCapacityHandler_OpenAIUsesPlatformWideSum(t *testing.T) {
	stub := &capacityReaderStub{platformSum: 120}
	h := NewEdgeCapacityHandler(stub)
	w := performCapacityRequest(t, h, "?platform=openai")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data struct {
			Platform         string `json:"platform"`
			TotalConcurrency int64  `json:"total_concurrency"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "openai", env.Data.Platform)
	require.Equal(t, int64(120), env.Data.TotalConcurrency)
	require.Equal(t, "openai", stub.lastPlatform)
	require.Zero(t, stub.lastGroupID)
}

func TestEdgeCapacityHandler_OpenAIGroupScopeCallerUsesGroupSum(t *testing.T) {
	gin.SetMode(gin.TestMode)
	groupID := int64(42)
	stub := &capacityReaderStub{platformGroupSum: 120}
	h := NewEdgeCapacityHandler(stub)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/edge/scheduling-capacity?platform=openai&group_scope=caller", nil)
	c.Set(middleware.EdgeCallerAPIKeyCtxKey, &service.APIKey{
		ID:          9,
		GroupID:     &groupID,
		RoutingMode: service.RoutingModeDirect,
	})
	h.GetSchedulingCapacity(c)
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, "openai", stub.lastPlatform)
	require.Equal(t, int64(42), stub.lastGroupID)

	var env struct {
		Data struct {
			TotalConcurrency int64 `json:"total_concurrency"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, int64(120), env.Data.TotalConcurrency)
}

func TestEdgeCapacityHandler_KiroUsesPlatformWideSum(t *testing.T) {
	// kiro mirrors the edge's whole schedulable kiro pool (no "default"-group
	// scoping), so it must read SumConcurrencyByPlatform("kiro"), never the
	// anthropic-group sum that would (wrongly) hand the kiro stub the anthropic
	// number — the bug this fix closes.
	stub := &capacityReaderStub{sum: 22, platformSum: 6}
	h := NewEdgeCapacityHandler(stub)
	w := performCapacityRequest(t, h, "?platform=kiro")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data struct {
			Platform         string `json:"platform"`
			TotalConcurrency int64  `json:"total_concurrency"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "kiro", env.Data.Platform)
	require.Equal(t, int64(6), env.Data.TotalConcurrency)
	require.Equal(t, "kiro", stub.lastPlatform)
	// The anthropic-group path must NOT have been taken for a kiro request.
	require.Empty(t, stub.lastGroup)
}

func TestEdgeCapacityHandler_RepoErrorIs500(t *testing.T) {
	h := NewEdgeCapacityHandler(&capacityReaderStub{err: errors.New("db down")})
	w := performCapacityRequest(t, h, "?platform=anthropic")
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
