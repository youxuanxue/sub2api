//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type aggregatorStub struct {
	session *service.EdgeAdminSession
	err     error
}

func (s aggregatorStub) Aggregate(_ context.Context, _ string) (*service.EdgeAccountsAggregate, error) {
	return &service.EdgeAccountsAggregate{}, nil
}

func (s aggregatorStub) MintAdminSession(_ context.Context, _ string) (*service.EdgeAdminSession, error) {
	return s.session, s.err
}

func performMintRequest(t *testing.T, h *EdgeAccountsHandler, edge string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "edge", Value: edge}}
	c.Request = httptest.NewRequest(http.MethodPost, "/api/v1/admin/edge-accounts/"+edge+"/admin-session", nil)
	h.MintAdminSession(c)
	return w
}

func TestMintAdminSession_Success(t *testing.T) {
	h := NewEdgeAccountsHandler(aggregatorStub{session: &service.EdgeAdminSession{
		EdgeID:    "us1",
		BaseURL:   "https://api-us1.tokenkey.dev",
		Token:     "jwt.token.value",
		ExpiresIn: 300,
	}})
	w := performMintRequest(t, h, "us1")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data adminSessionResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "us1", env.Data.EdgeID)
	require.Equal(t, 300, env.Data.ExpiresIn)
	// Token MUST ride in the fragment (after #), never as a query param.
	require.Contains(t, env.Data.HandoffURL, "https://api-us1.tokenkey.dev/admin/edge-handoff#")
	require.Contains(t, env.Data.HandoffURL, "tk_session=jwt.token.value")
	frag := env.Data.HandoffURL[strings.Index(env.Data.HandoffURL, "#"):]
	require.Contains(t, frag, "tk_session=")
	require.NotContains(t, env.Data.HandoffURL[:strings.Index(env.Data.HandoffURL, "#")], "tk_session")
}

func TestMintAdminSession_UnknownEdgeIs404(t *testing.T) {
	h := NewEdgeAccountsHandler(aggregatorStub{err: service.ErrEdgeNotFound})
	w := performMintRequest(t, h, "nope")
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestMintAdminSession_EdgeFailureIsBadGateway(t *testing.T) {
	h := NewEdgeAccountsHandler(aggregatorStub{err: context.DeadlineExceeded})
	w := performMintRequest(t, h, "us1")
	require.Equal(t, http.StatusBadGateway, w.Code)
}

func TestMintAdminSession_MissingEdgeIs400(t *testing.T) {
	h := NewEdgeAccountsHandler(aggregatorStub{})
	w := performMintRequest(t, h, "")
	require.Equal(t, http.StatusBadRequest, w.Code)
}
