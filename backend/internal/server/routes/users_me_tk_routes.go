package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"

	"github.com/gin-gonic/gin"
)

// registerTKUsersMeRoutes wires TokenKey-only user-scope endpoints under
// the REST-conventional `/users/me/*` namespace. The path shape matches
// the M0 dual-CC client contract (issue #59) verbatim — keeps the M0
// side at zero changes and avoids duplicating both `/user/qa/export`
// and `/users/me/qa/export` for the same intent (one canonical path).
//
// Companion file per CLAUDE.md §5 so RegisterUserRoutes only carries
// one helper call and upstream merges to user.go don't conflict with
// TK surface additions.
//
// Endpoints:
//   - POST /api/v1/users/me/qa/export — issue #59 Gap 1.
//     Auth: user-scope JWT (NOT admin). Service-layer enforces
//     `WHERE user_id = subject.UserID` so authenticated users can never
//     read another user's qa_records even by guessing synth_session_id.
func registerTKUsersMeRoutes(authenticated *gin.RouterGroup, h *handler.Handlers) {
	if h == nil || h.QA == nil {
		return
	}
	usersMe := authenticated.Group("/users/me")
	{
		qa := usersMe.Group("/qa")
		{
			qa.POST("/export", h.QA.ExportSelf)
		}
	}
}
