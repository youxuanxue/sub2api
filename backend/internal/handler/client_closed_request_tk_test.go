package handler

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestWriteReadRequestBodyError_ContextCanceledWrites499(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	writeReadRequestBodyError(c, context.Canceled, func(c *gin.Context, status int, errType, message string) {
		c.JSON(status, gin.H{"type": errType, "message": message})
	})

	require.Equal(t, statusClientClosedRequest, rec.Code)
	require.True(t, service.HasOpsClientClosedRequest(c))
	require.Contains(t, rec.Body.String(), "context canceled")
}

func TestWriteReadRequestBodyError_RegularReadErrorStays400(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	writeReadRequestBodyError(c, http.ErrBodyNotAllowed, func(c *gin.Context, status int, errType, message string) {
		c.JSON(status, gin.H{"type": errType, "message": message})
	})

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.False(t, service.HasOpsClientClosedRequest(c))
	require.Contains(t, rec.Body.String(), "Failed to read request body")
}

func TestWriteReadRequestBodyError_MaxBytesStays413(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)

	writeReadRequestBodyError(c, fmt.Errorf("read body: %w", &http.MaxBytesError{Limit: 4}), func(c *gin.Context, status int, errType, message string) {
		c.JSON(status, gin.H{"type": errType, "message": message})
	})

	require.Equal(t, http.StatusRequestEntityTooLarge, rec.Code)
	require.False(t, service.HasOpsClientClosedRequest(c))
	require.Contains(t, rec.Body.String(), "Request body too large")
}
