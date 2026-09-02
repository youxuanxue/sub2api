//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAICompatFirstAttemptSelectionFailure pins the SSOT contract for
// openai/newapi compat routes. Embeddings prod regression (newapi group +
// text-embedding-3-small not onboarded) is one case in this matrix.
func TestOpenAICompatFirstAttemptSelectionFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)

	newCtx := func(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/embeddings", nil)
		return c, w
	}

	type errorBody struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}

	writeJSON := func(t *testing.T, w *httptest.ResponseRecorder, status int, errType, msg string) {
		t.Helper()
		w.WriteHeader(status)
		_, _ = w.WriteString(fmt.Sprintf(`{"error":{"type":%q,"message":%q}}`, errType, msg))
	}

	t.Run("unsupported model -> 400 invalid_request_error", func(t *testing.T) {
		c, w := newCtx(t)
		groupID := int64(18)
		apiKey := &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformNewAPI},
		}
		err := fmt.Errorf("%w: deepseek-chat (total=10 eligible=0 model_unsupported=10)", service.ErrUnsupportedModel)

		status, errType, msg := openAICompatFirstAttemptSelectionFailure(c, nil, apiKey, "deepseek-chat", "deepseek-chat", err)
		writeJSON(t, w, status, errType, msg)

		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, w.Header().Get("Retry-After"))
		var body errorBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, service.TkUnsupportedModelErrType, body.Error.Type)
		assert.Equal(t, service.TkUnsupportedModelMessage("deepseek-chat"), body.Error.Message)
	})

	t.Run("embeddings regression newapi group model not in mapping -> 404 model_not_found", func(t *testing.T) {
		c, w := newCtx(t)
		groupID := int64(18)
		apiKey := &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformNewAPI},
		}
		fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{
			HasAccountsInPool: true,
			HasModelSupport:   false,
		}}
		err := fmt.Errorf("%w for platform %q", service.ErrNoAvailableAccounts, service.PlatformNewAPI)

		status, errType, msg := openAICompatFirstAttemptSelectionFailure(c, fd, apiKey, "text-embedding-3-small", "text-embedding-3-small", err)
		writeJSON(t, w, status, errType, msg)

		require.Equal(t, http.StatusNotFound, w.Code)
		assert.Empty(t, w.Header().Get("Retry-After"))
		var body errorBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "model_not_found", body.Error.Type)
		assert.Contains(t, body.Error.Message, "text-embedding-3-small")
		require.Len(t, fd.calls, 1)
		assert.Equal(t, service.PlatformNewAPI, fd.calls[0].Platform)
		assert.False(t, isOpsRoutingCapacityLimited(c))
	})

	t.Run("chat-style err path same mapping gap -> 404 not 429", func(t *testing.T) {
		c, _ := newCtx(t)
		groupID := int64(18)
		apiKey := &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformNewAPI},
		}
		fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{
			HasAccountsInPool: true,
			HasModelSupport:   false,
		}}
		err := fmt.Errorf("%w for platform %q", service.ErrNoAvailableAccounts, service.PlatformNewAPI)

		status, errType, msg := openAICompatFirstAttemptSelectionFailure(c, fd, apiKey, "gpt-4o", "gpt-4o", err)

		require.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, "model_not_found", errType)
		assert.Contains(t, msg, "gpt-4o")
	})

	t.Run("genuine empty pool -> 429 with Retry-After", func(t *testing.T) {
		c, _ := newCtx(t)
		groupID := int64(2)
		apiKey := &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
		}

		status, errType, msg := openAICompatFirstAttemptSelectionFailure(c, nil, apiKey, "text-embedding-3-small", "text-embedding-3-small", service.ErrNoAvailableAccounts)

		require.Equal(t, http.StatusTooManyRequests, status)
		assert.Equal(t, tkNoAvailableAccountsRetryAfterSeconds, c.Writer.Header().Get("Retry-After"))
		assert.Equal(t, "api_error", errType)
		assert.Contains(t, msg, "No available accounts")
	})

	t.Run("nil selection without err -> 404 when mapping gap", func(t *testing.T) {
		c, _ := newCtx(t)
		groupID := int64(18)
		apiKey := &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformNewAPI},
		}
		fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{
			HasAccountsInPool: true,
			HasModelSupport:   false,
		}}

		status, errType, _ := openAICompatFirstAttemptSelectionFailure(c, fd, apiKey, "gpt-4o", "gpt-4o", nil)

		require.Equal(t, http.StatusNotFound, status)
		assert.Equal(t, "model_not_found", errType)
	})

	t.Run("nil selection with genuine empty pool -> 429 with Retry-After", func(t *testing.T) {
		c, _ := newCtx(t)
		groupID := int64(18)
		apiKey := &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformNewAPI},
		}
		fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{
			HasAccountsInPool: false,
			HasModelSupport:   false,
		}}

		status, errType, msg := openAICompatFirstAttemptSelectionFailure(c, fd, apiKey, "gpt-4o", "gpt-4o", nil)

		require.Equal(t, http.StatusTooManyRequests, status)
		assert.Equal(t, "api_error", errType)
		assert.Equal(t, "No available accounts", msg)
		assert.Equal(t, tkNoAvailableAccountsRetryAfterSeconds, c.Writer.Header().Get("Retry-After"))
		assert.True(t, isOpsRoutingCapacityLimited(c))
	})

	t.Run("scheduler fault stays 503 even when mapping is absent", func(t *testing.T) {
		c, _ := newCtx(t)
		groupID := int64(18)
		apiKey := &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformNewAPI},
		}
		fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{
			HasAccountsInPool: true,
			HasModelSupport:   false,
		}}

		status, errType, msg := openAICompatFirstAttemptSelectionFailure(
			c, fd, apiKey, "text-embedding-3-small", "text-embedding-3-small", errors.New("query accounts failed: context deadline exceeded"),
		)

		require.Equal(t, http.StatusServiceUnavailable, status)
		assert.Equal(t, "api_error", errType)
		assert.Equal(t, "Service temporarily unavailable", msg)
		assert.Empty(t, fd.calls, "concrete scheduler faults must not be re-diagnosed as mapping gaps")
	})

	t.Run("deprecated model stays 400 before mapping diagnosis", func(t *testing.T) {
		c, _ := newCtx(t)
		groupID := int64(2)
		apiKey := &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
		}
		fd := &fakeDiagnoser{resp: service.ModelAvailabilityDiagnosis{
			HasAccountsInPool: true,
			HasModelSupport:   false,
		}}
		err := fmt.Errorf("%w: gpt-5.2", service.ErrDeprecatedOpenAIModel)

		status, errType, msg := openAICompatFirstAttemptSelectionFailure(c, fd, apiKey, "gpt-5.2", "gpt-5.2", err)

		require.Equal(t, http.StatusBadRequest, status)
		assert.Equal(t, service.TkDeprecatedOpenAIErrorType, errType)
		assert.Contains(t, msg, "gpt-5.5")
		assert.Empty(t, fd.calls, "client-owned model errors must win before mapping diagnosis")
	})
}

// TestOpenAICompatSelectionFailure_WrongPlatformDiagnosis documents the prod bug:
// diagnosing a newapi group with PlatformOpenAI returned 503 instead of 404.
func TestOpenAICompatSelectionFailure_WrongPlatformDiagnosis(t *testing.T) {
	c := newTestGinContextWithRequest()
	fd := &platformAwareFakeDiagnoser{byPlatform: map[string]service.ModelAvailabilityDiagnosis{
		service.PlatformOpenAI: {HasAccountsInPool: false, HasModelSupport: false},
		service.PlatformNewAPI: {HasAccountsInPool: true, HasModelSupport: false},
	}}
	groupID := int64(18)
	apiKey := &service.APIKey{
		GroupID: &groupID,
		Group:   &service.Group{ID: groupID, Platform: service.PlatformNewAPI},
	}

	wrong := classifyNoAccountErrorFromGin(c, fd, apiKey, "text-embedding-3-small", "text-embedding-3-small", service.PlatformOpenAI)
	require.Equal(t, http.StatusServiceUnavailable, wrong.Status)
	require.False(t, wrong.ModelNotFound)

	fd.calls = nil
	status, errType, _ := openAICompatFirstAttemptSelectionFailure(c, fd, apiKey, "text-embedding-3-small", "text-embedding-3-small", service.ErrNoAvailableAccounts)
	require.Equal(t, http.StatusNotFound, status)
	require.Equal(t, "model_not_found", errType)
	require.Len(t, fd.calls, 1)
	require.Equal(t, service.PlatformNewAPI, fd.calls[0].Platform)
}

type platformAwareFakeDiagnoser struct {
	byPlatform map[string]service.ModelAvailabilityDiagnosis
	calls      []fakeDiagnoseCall
}

func (f *platformAwareFakeDiagnoser) DiagnoseModelAvailabilityForPlatform(
	_ context.Context,
	groupID *int64,
	model, platform string,
) service.ModelAvailabilityDiagnosis {
	f.calls = append(f.calls, fakeDiagnoseCall{GroupID: groupID, Model: model, Platform: platform})
	if f.byPlatform == nil {
		return service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
	}
	if d, ok := f.byPlatform[platform]; ok {
		return d
	}
	return service.ModelAvailabilityDiagnosis{HasAccountsInPool: true, HasModelSupport: true}
}
