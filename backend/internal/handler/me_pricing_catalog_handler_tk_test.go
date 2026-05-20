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
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeMePricingSvc struct {
	resp    *service.MePricingCatalogResponse
	err     error
	seenOpt service.MePricingCatalogOptions
	seenUID int64
}

func (f *fakeMePricingSvc) BuildForUser(
	_ context.Context, userID int64, opts service.MePricingCatalogOptions,
) (*service.MePricingCatalogResponse, error) {
	f.seenOpt = opts
	f.seenUID = userID
	return f.resp, f.err
}

func newMePricingRouter(t *testing.T, src MePricingCatalogSource, withAuth bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	h := &MePricingCatalogHandler{svc: src}
	r := gin.New()
	r.GET("/api/v1/me/pricing-catalog", func(c *gin.Context) {
		if withAuth {
			c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 42})
		}
		h.Get(c)
	})
	return r
}

func TestMePricingHandler_RequiresAuth(t *testing.T) {
	r := newMePricingRouter(t, &fakeMePricingSvc{}, false)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/pricing-catalog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestMePricingHandler_OK(t *testing.T) {
	svc := &fakeMePricingSvc{resp: &service.MePricingCatalogResponse{
		TargetGroup: service.MePricingTargetGroup{ID: 10, Name: "Pro", Platform: "newapi", RateMultiplier: 1.0, ListMultiplier: 1.0},
		Models:      []service.MePricingModel{{ModelID: "gpt-4o", Capabilities: []string{"vision"}}},
		MyKeys:      []service.MePricingKeyRef{},
		AccessibleGroups: []service.MePricingGroupRef{
			{ID: 10, Name: "Pro", Platform: "newapi", RateMultiplier: 1.0, IsCurrentForKey: true},
		},
	}}
	r := newMePricingRouter(t, svc, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/pricing-catalog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Code int                                 `json:"code"`
		Data *service.MePricingCatalogResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data)
	assert.Equal(t, int64(10), resp.Data.TargetGroup.ID)
	require.Len(t, resp.Data.Models, 1)
	assert.Equal(t, "gpt-4o", resp.Data.Models[0].ModelID)
	assert.Equal(t, int64(42), svc.seenUID)
}

func TestMePricingHandler_ParsesAPIKeyAndGroupParams(t *testing.T) {
	svc := &fakeMePricingSvc{resp: &service.MePricingCatalogResponse{}}
	r := newMePricingRouter(t, svc, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/pricing-catalog?api_key_id=11&group_id=22", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NotNil(t, svc.seenOpt.APIKeyID)
	require.NotNil(t, svc.seenOpt.GroupID)
	assert.Equal(t, int64(11), *svc.seenOpt.APIKeyID)
	assert.Equal(t, int64(22), *svc.seenOpt.GroupID)
}

func TestMePricingHandler_RejectsInvalidParams(t *testing.T) {
	svc := &fakeMePricingSvc{resp: &service.MePricingCatalogResponse{}}
	r := newMePricingRouter(t, svc, true)
	cases := []string{
		"/api/v1/me/pricing-catalog?api_key_id=abc",
		"/api/v1/me/pricing-catalog?api_key_id=0",
		"/api/v1/me/pricing-catalog?group_id=-1",
	}
	for _, url := range cases {
		req := httptest.NewRequest(http.MethodGet, url, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		assert.Equal(t, http.StatusBadRequest, w.Code, url)
	}
}

func TestMePricingHandler_MapsServiceErrors(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		wantCode int
	}{
		{"forbidden", service.ErrMePricingGroupForbidden, http.StatusForbidden},
		{"not_found", service.ErrMePricingAPIKeyNotFound, http.StatusNotFound},
		{"conflict", service.ErrMePricingConflictingTargets, http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &fakeMePricingSvc{err: tc.err}
			r := newMePricingRouter(t, svc, true)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/me/pricing-catalog", nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)
			assert.Equal(t, tc.wantCode, w.Code)
		})
	}
}

func TestMePricingHandler_NoAccessibleGroupsRendersEmptyOK(t *testing.T) {
	// Empty-keys / no-subscription user: 200 + empty arrays so the UI
	// can show the "create a key" banner without an error toast.
	svc := &fakeMePricingSvc{err: service.ErrMePricingNoAccessibleGroups}
	r := newMePricingRouter(t, svc, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/pricing-catalog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	var resp struct {
		Code    int                               `json:"code"`
		Message string                            `json:"message"`
		Data    *service.MePricingCatalogResponse `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotNil(t, resp.Data)
	assert.Empty(t, resp.Data.Models)
	assert.Empty(t, resp.Data.AccessibleGroups)
	assert.Empty(t, resp.Data.MyKeys)
	// Envelope MUST match response.Success ({code:0, message:"success"}).
	assert.Equal(t, 0, resp.Code)
	assert.Equal(t, "success", resp.Message)
}

func TestMePricingHandler_NilServiceReturns500(t *testing.T) {
	r := newMePricingRouter(t, nil, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/pricing-catalog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}

func TestMePricingHandler_GenericErrorIs500(t *testing.T) {
	svc := &fakeMePricingSvc{err: errors.New("boom")}
	r := newMePricingRouter(t, svc, true)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me/pricing-catalog", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	assert.Equal(t, http.StatusInternalServerError, w.Code)
}
