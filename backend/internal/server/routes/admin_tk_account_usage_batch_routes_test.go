package routes

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	adminhandler "github.com/Wei-Shaw/sub2api/internal/handler/admin"
	servermiddleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/gin-gonic/gin"
)

func TestRegisterAdminRoutesUsageBatchDoesNotDuplicateHandlers(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")

	// Duplicate POST /admin/accounts/usage/batch registration panics at startup.
	RegisterAdminRoutes(
		v1,
		&handler.Handlers{
			Admin: &handler.AdminHandlers{
				Account: &adminhandler.AccountHandler{},
			},
		},
		servermiddleware.AdminAuthMiddleware(func(c *gin.Context) { c.Next() }),
		servermiddleware.AuditLogMiddleware(func(c *gin.Context) { c.Next() }),
		servermiddleware.StepUpAuthMiddleware(func(c *gin.Context) { c.Next() }),
		nil,
		nil,
	)
}
