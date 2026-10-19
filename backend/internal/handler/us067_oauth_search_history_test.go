package handler

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestUS067_NativeResponsesRetainsFullDiagnosticInput(t *testing.T) {
	gin.SetMode(gin.TestMode)
	// Stop at model validation after the real ingress read, before routing or a
	// paid upstream call. The diagnostic needs the exact body, including its tail.
	body := `{"input":[{"role":"user","content":"` + strings.Repeat("private", 50000) + `"},{"type":"web_search_call"}]}`
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	groupID := int64(2)
	c.Set(string(middleware.ContextKeyAPIKey), &service.APIKey{ID: 101, GroupID: &groupID, User: &service.User{ID: 1}})
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 1, Concurrency: 1})
	h := newOpenAIHandlerForPreviousResponseIDValidation(t, nil)
	h.Responses(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "model is required")
	retained, ok := c.Get(service.OpsRequestBodyKey)
	require.True(t, ok)
	require.Equal(t, []byte(body), retained)
	require.NotContains(t, w.Body.String(), "private")
}
