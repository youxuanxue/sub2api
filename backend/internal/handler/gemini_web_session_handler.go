package handler

import (
	"context"
	"net/http"
	"regexp"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// geminiWebRuntimeStore is deliberately narrower than AccountRepository. The
// worker may replace only one account's mutable runtime through a CAS write.
type geminiWebRuntimeStore interface {
	GetByID(context.Context, int64) (*service.Account, error)
	ListDueGeminiWebAccounts(context.Context) ([]int64, error)
	CompareAndSwapGeminiWebRuntime(context.Context, int64, int64, string, map[string]any) (bool, error)
	AcquireGeminiWebLease(context.Context, int64, string) (bool, error)
	ReleaseGeminiWebLease(context.Context, int64, string) error
}

type GeminiWebSessionHandler struct {
	store geminiWebRuntimeStore
}

func NewGeminiWebSessionHandler(accounts service.AccountRepository) *GeminiWebSessionHandler {
	store, _ := accounts.(geminiWebRuntimeStore)
	return &GeminiWebSessionHandler{store: store}
}

func (h *GeminiWebSessionHandler) Enabled() bool {
	return h != nil && h.store != nil
}

func (h *GeminiWebSessionHandler) authorize(c *gin.Context) bool {
	if !h.Enabled() {
		c.Status(http.StatusNotFound)
		return false
	}
	if _, ok := c.Get(middleware.EdgeCallerAPIKeyCtxKey); !ok {
		c.Status(http.StatusUnauthorized)
		return false
	}
	return true
}

func geminiWebRuntime(account *service.Account) (map[string]any, bool) {
	if account == nil || account.Platform != service.PlatformGemini || account.Type != service.AccountTypeAPIKey {
		return nil, false
	}
	geminiWeb, ok := account.Credentials["gemini_web"].(map[string]any)
	if !ok {
		return nil, false
	}
	runtime, ok := geminiWeb["runtime"].(map[string]any)
	if !ok || len(runtime) == 0 {
		return nil, false
	}
	return runtime, true
}

func (h *GeminiWebSessionHandler) session(c *gin.Context) (*service.Account, map[string]any, bool) {
	if !h.authorize(c) {
		return nil, nil, false
	}
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		c.Status(http.StatusBadRequest)
		return nil, nil, false
	}
	account, err := h.store.GetByID(c.Request.Context(), id)
	if err != nil || account == nil || account.Status != service.StatusActive || !account.Schedulable {
		c.Status(http.StatusNotFound)
		return nil, nil, false
	}
	runtime, ok := geminiWebRuntime(account)
	if !ok {
		c.Status(http.StatusNotFound)
		return nil, nil, false
	}
	return account, runtime, true
}

func (h *GeminiWebSessionHandler) Get(c *gin.Context) {
	account, runtime, ok := h.session(c)
	if !ok {
		return
	}
	c.Header("Cache-Control", "no-store")
	c.JSON(http.StatusOK, gin.H{"account_id": account.ID, "api_key": account.Credentials["api_key"], "concurrency": account.Concurrency, "runtime": runtime})
}

func (h *GeminiWebSessionHandler) PutRuntime(c *gin.Context) {
	account, _, ok := h.session(c)
	if !ok {
		return
	}
	var request struct {
		ExpectedVersion int64          `json:"expected_version"`
		Runtime         map[string]any `json:"runtime"`
	}
	if c.ShouldBindJSON(&request) != nil || request.ExpectedVersion < 0 || len(request.Runtime) == 0 {
		c.Status(http.StatusBadRequest)
		return
	}
	owner := c.GetHeader("X-Gemini-Web-Lease")
	if !geminiWebLeaseOwner.MatchString(owner) {
		c.Status(http.StatusBadRequest)
		return
	}
	updated, err := h.store.CompareAndSwapGeminiWebRuntime(c.Request.Context(), account.ID, request.ExpectedVersion, owner, request.Runtime)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if !updated {
		c.Status(http.StatusConflict)
		return
	}
	request.Runtime["version"] = request.ExpectedVersion + 1
	c.JSON(http.StatusOK, gin.H{"account_id": account.ID, "runtime": request.Runtime})
}

// GeminiWebControlProtocolVersion is the control-plane contract this backend
// speaks to the Gemini Web Worker. The Worker deploys on its own cadence, so a
// bump here MUST be paired with CONTROL_PROTOCOL_VERSION in ops/gemini-web
// worker.py; scripts/checks/gemini-web-control-protocol.py fails preflight when
// the two drift. The Worker refuses an unrecognised version rather than guessing,
// so drift takes the fleet out of service instead of corrupting sessions.
const GeminiWebControlProtocolVersion = 1

// WarmAccounts returns only account references. The worker loads each session
// through Get, keeping bulk scheduling metadata free of cookies.
func (h *GeminiWebSessionHandler) WarmAccounts(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	ids, err := h.store.ListDueGeminiWebAccounts(c.Request.Context())
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	c.JSON(http.StatusOK, gin.H{"accounts": ids, "protocol_version": GeminiWebControlProtocolVersion})
}

var geminiWebLeaseOwner = regexp.MustCompile(`^[a-f0-9]{32}$`)

// Lease ownership lives in the same database row as runtime CAS, so an expired
// worker cannot publish over its successor, even across edge processes.
func (h *GeminiWebSessionHandler) Lease(c *gin.Context) {
	account, _, ok := h.session(c)
	if !ok {
		return
	}
	owner := c.GetHeader("X-Gemini-Web-Lease")
	if !geminiWebLeaseOwner.MatchString(owner) {
		c.Status(http.StatusBadRequest)
		return
	}
	if c.Request.Method == http.MethodDelete {
		if err := h.store.ReleaseGeminiWebLease(c.Request.Context(), account.ID, owner); err != nil {
			c.Status(http.StatusInternalServerError)
			return
		}
		c.JSON(http.StatusOK, gin.H{})
		return
	}
	acquired, err := h.store.AcquireGeminiWebLease(c.Request.Context(), account.ID, owner)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	if !acquired {
		c.Status(http.StatusConflict)
		return
	}
	c.JSON(http.StatusOK, gin.H{})
}
