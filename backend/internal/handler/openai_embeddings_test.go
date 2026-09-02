//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenAIEmbeddingsSelectionFailureResponse pins embeddings account-selection
// failures to the same client-facing contract as openai/newapi chat: unsupported
// model names → 400; model absent from group mapping on newapi groups → 404
// model_not_found (not 503 internal from a wrong PlatformOpenAI diagnosis).
func TestOpenAIEmbeddingsSelectionFailureResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h := &OpenAIGatewayHandler{}

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

	t.Run("unsupported model -> 400 invalid_request_error", func(t *testing.T) {
		c, w := newCtx(t)
		groupID := int64(18)
		apiKey := &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformNewAPI},
		}
		err := fmt.Errorf("%w: text-embedding-3-small (total=10 eligible=0 model_unsupported=10)", service.ErrUnsupportedModel)

		h.respondOpenAIEmbeddingsAccountSelectionFailure(c, apiKey, "text-embedding-3-small", err)

		require.Equal(t, http.StatusBadRequest, w.Code)
		assert.Empty(t, w.Header().Get("Retry-After"))
		var body errorBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, service.TkUnsupportedModelErrType, body.Error.Type)
		assert.Equal(t, service.TkUnsupportedModelMessage("text-embedding-3-small"), body.Error.Message)
	})

	t.Run("newapi group model not in mapping -> 404 model_not_found", func(t *testing.T) {
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
		h := &OpenAIGatewayHandler{}
		err := fmt.Errorf("%w for platform %q", service.ErrNoAvailableAccounts, service.PlatformNewAPI)

		h.respondOpenAIEmbeddingsAccountSelectionFailureWithDiagnoser(c, apiKey, "text-embedding-3-small", err, fd)

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

	t.Run("genuine empty pool -> 429 with Retry-After", func(t *testing.T) {
		c, w := newCtx(t)
		groupID := int64(2)
		apiKey := &service.APIKey{
			GroupID: &groupID,
			Group:   &service.Group{ID: groupID, Platform: service.PlatformOpenAI},
		}
		h := &OpenAIGatewayHandler{}

		h.respondOpenAIEmbeddingsAccountSelectionFailure(c, apiKey, "text-embedding-3-small", service.ErrNoAvailableAccounts)

		require.Equal(t, http.StatusTooManyRequests, w.Code)
		assert.Equal(t, tkNoAvailableAccountsRetryAfterSeconds, w.Header().Get("Retry-After"))
		var body errorBody
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, "api_error", body.Error.Type)
		assert.Contains(t, body.Error.Message, "No available accounts")
	})
}

// TestOpenAIEmbeddingsRegression_WrongPlatformDiagnosis documents the prod bug:
// diagnosing a newapi group with PlatformOpenAI returned 503 instead of 404.
func TestOpenAIEmbeddingsRegression_WrongPlatformDiagnosis(t *testing.T) {
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

	right := classifyOpenAICompatibleNoAccountErrorFromGin(c, fd, apiKey, "text-embedding-3-small", "text-embedding-3-small")
	require.Equal(t, http.StatusNotFound, right.Status)
	require.True(t, right.ModelNotFound)
	require.Len(t, fd.calls, 2)
	require.Equal(t, service.PlatformOpenAI, fd.calls[0].Platform)
	require.Equal(t, service.PlatformNewAPI, fd.calls[1].Platform)
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
