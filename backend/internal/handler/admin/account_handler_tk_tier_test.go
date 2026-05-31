//go:build unit

package admin

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// These tests exercise the ApplyTier HTTP handler's request-validation and
// nil-service guards directly (constructing AccountHandler in-package). The
// tier-binding business logic is covered by service.AccountTierService tests.

func newTierApplyContext(t *testing.T, idParam, body string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Params = gin.Params{{Key: "id", Value: idParam}}
	c.Request = httptest.NewRequest(http.MethodPost, "/admin/accounts/"+idParam+"/apply-tier", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, w
}

func TestApplyTierHandler_InvalidID(t *testing.T) {
	h := &AccountHandler{}
	c, w := newTierApplyContext(t, "not-an-int", `{"tier":"l4"}`)
	h.ApplyTier(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestApplyTierHandler_MissingTier(t *testing.T) {
	h := &AccountHandler{}
	c, w := newTierApplyContext(t, "7", `{}`)
	h.ApplyTier(c)
	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestApplyTierHandler_NilServiceIs500(t *testing.T) {
	h := &AccountHandler{} // accountTierService nil
	c, w := newTierApplyContext(t, "7", `{"tier":"l4"}`)
	h.ApplyTier(c)
	require.Equal(t, http.StatusInternalServerError, w.Code)
}
