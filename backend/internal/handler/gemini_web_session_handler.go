package handler

import (
	"context"
	"crypto/subtle"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// geminiWebRuntimeStore is deliberately narrower than AccountRepository. The
// worker may replace only one account's mutable runtime through a CAS write.
type geminiWebRuntimeStore interface {
	GetByID(context.Context, int64) (*service.Account, error)
	ListByPlatform(context.Context, string) ([]service.Account, error)
	CompareAndSwapGeminiWebRuntime(context.Context, int64, int64, map[string]any) (bool, error)
}

type GeminiWebSessionHandler struct {
	store geminiWebRuntimeStore
	token string
}

func NewGeminiWebSessionHandler(accounts service.AccountRepository) *GeminiWebSessionHandler {
	store, _ := accounts.(geminiWebRuntimeStore)
	return &GeminiWebSessionHandler{store: store, token: strings.TrimSpace(os.Getenv("GEMINI_WEB_CONTROL_TOKEN"))}
}

func (h *GeminiWebSessionHandler) Enabled() bool {
	return h != nil && h.store != nil && len(h.token) >= 32
}

func (h *GeminiWebSessionHandler) authorize(c *gin.Context) bool {
	if !h.Enabled() {
		c.Status(http.StatusNotFound)
		return false
	}
	value := strings.TrimSpace(strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer "))
	if len(value) != len(h.token) || subtle.ConstantTimeCompare([]byte(value), []byte(h.token)) != 1 {
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
	c.JSON(http.StatusOK, gin.H{"account_id": account.ID, "runtime": runtime})
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
	updated, err := h.store.CompareAndSwapGeminiWebRuntime(c.Request.Context(), account.ID, request.ExpectedVersion, request.Runtime)
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

// WarmAccounts returns only account references. The worker loads each session
// through Get, keeping bulk scheduling metadata free of cookies.
func (h *GeminiWebSessionHandler) WarmAccounts(c *gin.Context) {
	if !h.authorize(c) {
		return
	}
	accounts, err := h.store.ListByPlatform(c.Request.Context(), service.PlatformGemini)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	ids := make([]int64, 0)
	for i := range accounts {
		runtime, ok := geminiWebRuntime(&accounts[i])
		if ok && accounts[i].Schedulable && accounts[i].Status == service.StatusActive && runtime != nil {
			ids = append(ids, accounts[i].ID)
		}
	}
	c.JSON(http.StatusOK, gin.H{"accounts": ids})
}
