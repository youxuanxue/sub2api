package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// registerTKUserRoutes wires every TokenKey-only authenticated endpoint
// reachable from RegisterUserRoutes' JWT-only `authenticated` group.
// One helper call from upstream-shaped user.go (CLAUDE.md §5) keeps the
// merge-conflict surface there minimal, and one helper file (this one)
// keeps every TK user-side route in a single Jobs-style canonical place
// — no scattered `*_tk_*.go` per route prefix.
//
// Endpoints (JWT-only):
//   - POST /api/v1/user/onboarding-tour-completed — US-031 P1-A.
//
// Endpoints requiring BOTH JWT and API-key auth live in
// registerTKUserDualAuthRoutes below.
func registerTKUserRoutes(authenticated, user *gin.RouterGroup, h *handler.Handlers) {
	user.POST("/onboarding-tour-completed", h.User.MarkOnboardingTourSeen)
	_ = authenticated // reserved for future JWT-only TK additions
}

// registerTKUserDualAuthRoutes wires TokenKey user-side endpoints that
// must accept EITHER user-scope JWT (browser / 用户中心) OR user-scope
// API key (SDK / CI callers like M0). Issue #63 — the M0 dual-CC client
// holds the same `sk-...` token it uses against `/v1/messages`; routing
// the QA self-export through JWT-only middleware made TokenKey reject
// its own canonical "developer credential" with HTTP 401.
//
// The handler layer is auth-source-agnostic — both branches of
// EitherAuth write the same AuthSubject{UserID} into context, so QA
// service reads `WHERE user_id = subject.UserID` are unaffected.
//
// Endpoints (JWT OR API-key):
//   - POST /api/v1/users/me/qa/export — issue #59 + #63.
//   - GET /api/v1/users/me/qa/exports/*key — issue #67 + #68 localfs download.
func registerTKUserDualAuthRoutes(
	v1 *gin.RouterGroup,
	h *handler.Handlers,
	eitherAuth middleware.EitherAuthMiddleware,
	settingService *service.SettingService,
) {
	dualAuth := v1.Group("")
	dualAuth.Use(gin.HandlerFunc(eitherAuth))
	dualAuth.Use(middleware.BackendModeUserGuard(settingService))
	{
		dualAuth.POST("/users/me/qa/export", h.QA.ExportSelf)
		dualAuth.GET("/users/me/qa/exports/*key", h.QA.DownloadSelfExport)
	}
}
