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
	session          *service.EdgeAdminSession
	err              error
	agg              *service.EdgeAccountsAggregate
	regularCalls     int
	freshCalls       int
	byStubCalls      int
	byStubFreshCalls int
}

func (s *aggregatorStub) Aggregate(_ context.Context, _ string) (*service.EdgeAccountsAggregate, error) {
	s.regularCalls++
	if s.agg != nil {
		return s.agg, nil
	}
	return &service.EdgeAccountsAggregate{}, nil
}

func (s *aggregatorStub) AggregateFresh(_ context.Context, _ string) (*service.EdgeAccountsAggregate, error) {
	s.freshCalls++
	if s.agg != nil {
		return s.agg, nil
	}
	return &service.EdgeAccountsAggregate{}, nil
}

func (s *aggregatorStub) AggregateByStub(_ context.Context) (*service.EdgeAccountsAggregate, error) {
	s.byStubCalls++
	if s.agg != nil {
		return s.agg, nil
	}
	return &service.EdgeAccountsAggregate{}, nil
}

func (s *aggregatorStub) AggregateByStubFresh(_ context.Context) (*service.EdgeAccountsAggregate, error) {
	s.byStubFreshCalls++
	if s.agg != nil {
		return s.agg, nil
	}
	return &service.EdgeAccountsAggregate{}, nil
}

func (s *aggregatorStub) MintAdminSession(_ context.Context, _ string) (*service.EdgeAdminSession, error) {
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
	h := NewEdgeAccountsHandler(&aggregatorStub{session: &service.EdgeAdminSession{
		EdgeID:       "us1",
		BaseURL:      "https://api-us1.tokenkey.dev",
		Token:        "jwt.token.value",
		RefreshToken: "rt_handoff_value",
		ExpiresIn:    3600,
	}})
	w := performMintRequest(t, h, "us1")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data adminSessionResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Equal(t, "us1", env.Data.EdgeID)
	require.Equal(t, 3600, env.Data.ExpiresIn)
	// Access token, refresh token, expires_in MUST all ride in the fragment
	// (after #), never as a query param.
	require.Contains(t, env.Data.HandoffURL, "https://api-us1.tokenkey.dev/admin/edge-handoff#")
	require.Contains(t, env.Data.HandoffURL, "tk_session=jwt.token.value")
	require.Contains(t, env.Data.HandoffURL, "refresh_token=rt_handoff_value")
	require.Contains(t, env.Data.HandoffURL, "expires_in=3600")
	hashIdx := strings.Index(env.Data.HandoffURL, "#")
	frag := env.Data.HandoffURL[hashIdx:]
	require.Contains(t, frag, "tk_session=")
	require.Contains(t, frag, "refresh_token=")
	path := env.Data.HandoffURL[:hashIdx]
	require.NotContains(t, path, "tk_session")
	require.NotContains(t, path, "refresh_token")
}

func TestMintAdminSession_OmitsRefreshWhenEmpty(t *testing.T) {
	// Backward-compat: an older edge that mints a single token (no refresh) still
	// yields a valid URL without dangling refresh_token=/expires_in= keys.
	h := NewEdgeAccountsHandler(&aggregatorStub{session: &service.EdgeAdminSession{
		EdgeID:  "us1",
		BaseURL: "https://api-us1.tokenkey.dev",
		Token:   "jwt.token.value",
	}})
	w := performMintRequest(t, h, "us1")
	require.Equal(t, http.StatusOK, w.Code)

	var env struct {
		Data adminSessionResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &env))
	require.Contains(t, env.Data.HandoffURL, "tk_session=jwt.token.value")
	require.NotContains(t, env.Data.HandoffURL, "refresh_token=")
	require.NotContains(t, env.Data.HandoffURL, "expires_in=")
}

func TestMintAdminSession_UnknownEdgeIs404(t *testing.T) {
	h := NewEdgeAccountsHandler(&aggregatorStub{err: service.ErrEdgeNotFound})
	w := performMintRequest(t, h, "nope")
	require.Equal(t, http.StatusNotFound, w.Code)
}

func TestMintAdminSession_EdgeFailureIsBadGateway(t *testing.T) {
	h := NewEdgeAccountsHandler(&aggregatorStub{err: context.DeadlineExceeded})
	w := performMintRequest(t, h, "us1")
	require.Equal(t, http.StatusBadGateway, w.Code)
}

func TestMintAdminSession_MissingEdgeIs400(t *testing.T) {
	h := NewEdgeAccountsHandler(&aggregatorStub{})
	w := performMintRequest(t, h, "")
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func performListRequest(t *testing.T, h *EdgeAccountsHandler, rawQuery string, ifNoneMatch string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	target := "/api/v1/admin/edge-accounts"
	if rawQuery == "" {
		rawQuery = "platform=all"
	}
	target += "?" + rawQuery
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if ifNoneMatch != "" {
		c.Request.Header.Set("If-None-Match", ifNoneMatch)
	}
	h.List(c)
	// Calling the handler directly bypasses the gin engine, which is what normally
	// flushes a body-less c.Status (e.g. the 304 path) to the underlying writer.
	// Flush explicitly so the recorder observes the real status code.
	c.Writer.WriteHeaderNow()
	return w
}

func TestList_SetsETagOn200(t *testing.T) {
	h := NewEdgeAccountsHandler(&aggregatorStub{agg: &service.EdgeAccountsAggregate{
		Platform: "all",
		Edges:    []service.EdgeAccountsResult{{EdgeID: "us1", BaseURL: "https://api-us1.tokenkey.dev", OK: true}},
		TS:       111,
	}})
	w := performListRequest(t, h, "", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.NotEmpty(t, w.Header().Get("ETag"))
	require.Equal(t, "If-None-Match", w.Header().Get("Vary"))
}

func TestList_NotModifiedWhenIfNoneMatchMatches(t *testing.T) {
	h := NewEdgeAccountsHandler(&aggregatorStub{agg: &service.EdgeAccountsAggregate{
		Platform: "all",
		Edges:    []service.EdgeAccountsResult{{EdgeID: "us1", BaseURL: "https://api-us1.tokenkey.dev", OK: true}},
		TS:       111,
	}})
	first := performListRequest(t, h, "", "")
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	second := performListRequest(t, h, "", etag)
	require.Equal(t, http.StatusNotModified, second.Code)
	require.Empty(t, second.Body.Bytes(), "304 carries no body")
}

// The ETag deliberately excludes the per-fan-out TS, so an unchanged inventory keeps
// the same ETag even after a background refresh bumps TS — otherwise the 304 path
// would never fire on a live page.
func TestList_ETagIgnoresTimestamp(t *testing.T) {
	edges := []service.EdgeAccountsResult{{EdgeID: "us1", BaseURL: "https://api-us1.tokenkey.dev", OK: true}}
	hA := NewEdgeAccountsHandler(&aggregatorStub{agg: &service.EdgeAccountsAggregate{Platform: "all", Edges: edges, TS: 111}})
	hB := NewEdgeAccountsHandler(&aggregatorStub{agg: &service.EdgeAccountsAggregate{Platform: "all", Edges: edges, TS: 999}})
	require.Equal(t, performListRequest(t, hA, "", "").Header().Get("ETag"), performListRequest(t, hB, "", "").Header().Get("ETag"))
}

func TestList_ForceUsesFreshAggregate(t *testing.T) {
	stub := &aggregatorStub{agg: &service.EdgeAccountsAggregate{Platform: "all"}}
	h := NewEdgeAccountsHandler(stub)

	w := performListRequest(t, h, "platform=all&force=1", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 0, stub.regularCalls)
	require.Equal(t, 1, stub.freshCalls)
}

func TestList_ByStubUsesFreshAggregate(t *testing.T) {
	stub := &aggregatorStub{agg: &service.EdgeAccountsAggregate{Platform: "__by_stub__"}}
	h := NewEdgeAccountsHandler(stub)

	w := performListRequest(t, h, "view=by-stub", "")
	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, 0, stub.byStubCalls)
	require.Equal(t, 1, stub.byStubFreshCalls)
}

func TestList_ByStubStillSupportsETagAfterFreshFanout(t *testing.T) {
	stub := &aggregatorStub{agg: &service.EdgeAccountsAggregate{
		Platform: "__by_stub__",
		Edges: []service.EdgeAccountsResult{{
			EdgeID: "us3", BaseURL: "https://api-us3.tokenkey.dev", OK: true, StubAccountID: 70,
		}},
	}}
	h := NewEdgeAccountsHandler(stub)

	first := performListRequest(t, h, "view=by-stub", "")
	require.Equal(t, http.StatusOK, first.Code)
	etag := first.Header().Get("ETag")
	require.NotEmpty(t, etag)

	second := performListRequest(t, h, "view=by-stub", etag)
	require.Equal(t, http.StatusNotModified, second.Code)
	require.Equal(t, 0, stub.byStubCalls)
	require.Equal(t, 2, stub.byStubFreshCalls, "by-stub must re-fan-out before deciding 304")
	require.Empty(t, second.Body.Bytes())
}
