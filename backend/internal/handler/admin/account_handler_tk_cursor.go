package admin

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/integration/cursor"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) cursorAdminClient(c *gin.Context) (*cursor.Client, string, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		response.Error(c, http.StatusUnauthorized, "Administrator identity required")
		return nil, "", false
	}
	client := h.cursorOAuth
	if !client.Enabled() {
		response.Error(c, http.StatusServiceUnavailable, "Cursor authorization is unavailable")
		return nil, "", false
	}
	return client, "admin:" + strconv.FormatInt(subject.UserID, 10), true
}
func cursorAdminError(c *gin.Context, err error) {
	status := http.StatusBadRequest
	var upstream *cursor.Error
	if errors.As(err, &upstream) {
		switch upstream.Status {
		case 404, 409, 410, 429:
			status = upstream.Status
		default:
			status = http.StatusBadGateway
		}
	}
	response.Error(c, status, err.Error())
}

func (h *AccountHandler) CursorCapabilities(c *gin.Context) {
	response.Success(c, gin.H{"enabled": h.cursorOAuth.Enabled()})
}
func (h *AccountHandler) StartCursorAuthorization(c *gin.Context) {
	client, owner, ok := h.cursorAdminClient(c)
	if !ok {
		return
	}
	result, err := client.Start(c.Request.Context(), owner)
	if err != nil {
		cursorAdminError(c, err)
		return
	}
	response.Success(c, result)
}
func (h *AccountHandler) GetCursorAuthorization(c *gin.Context) {
	client, owner, ok := h.cursorAdminClient(c)
	if !ok {
		return
	}
	result, err := client.Status(c.Request.Context(), owner, c.Param("session"))
	if err != nil {
		cursorAdminError(c, err)
		return
	}
	response.Success(c, result)
}
func (h *AccountHandler) CancelCursorAuthorization(c *gin.Context) {
	client, owner, ok := h.cursorAdminClient(c)
	if !ok {
		return
	}
	if err := client.Cancel(c.Request.Context(), owner, c.Param("session")); err != nil {
		cursorAdminError(c, err)
		return
	}
	response.Success(c, gin.H{"cancelled": true})
}
func (h *AccountHandler) ImportCursorAccount(c *gin.Context) {
	client, owner, ok := h.cursorAdminClient(c)
	if !ok {
		return
	}
	var input service.CursorAccountInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.Error(c, http.StatusBadRequest, "Invalid Cursor account request")
		return
	}
	result, err := executeAdminIdempotent(c, "admin.accounts.cursor.import", input, 24*time.Hour, func(ctx context.Context) (any, error) {
		account, err := service.ImportCursorAccount(ctx, h.adminService, client, owner, input)
		if err != nil {
			return nil, err
		}
		h.scheduleProtocolCapabilityProbes(account)
		return gin.H{"id": account.ID, "name": account.Name, "platform": account.Platform, "type": account.Type, "expires_at": account.ExpiresAt}, nil
	})
	if err != nil {
		cursorAdminError(c, err)
		return
	}
	if result.Replayed {
		c.Header("X-Idempotency-Replayed", "true")
	}
	response.Success(c, result.Data)
}
