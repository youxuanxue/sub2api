package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

func registerCursorAccountRoutes(accounts *gin.RouterGroup, h *handler.Handlers) {
	accounts.GET("/cursor/capabilities", h.Admin.Account.CursorCapabilities)
	accounts.POST("/cursor/authorizations", h.Admin.Account.StartCursorAuthorization)
	accounts.GET("/cursor/authorizations/:session", h.Admin.Account.GetCursorAuthorization)
	accounts.DELETE("/cursor/authorizations/:session", h.Admin.Account.CancelCursorAuthorization)
	accounts.POST("/cursor/import", h.Admin.Account.ImportCursorAccount)
}
