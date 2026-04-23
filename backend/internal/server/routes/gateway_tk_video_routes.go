package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/gin-gonic/gin"
)

// registerTKVideoRoutes registers the four async video task routes on a Gin
// route group that already has the OpenAI-compat middleware chain applied
// (auth, body limit, requireGroupAnthropic, …).
//
// The four paths come in two pairs because clients use either the new-api
// idiomatic `/video/generations` shape OR the OpenAI-Video API
// `/videos` shape; we accept both so SDKs from either ecosystem keep
// working without per-channel branching on the client side.
func registerTKVideoRoutes(g *gin.RouterGroup, h *handler.Handlers) {
	submit := tkOpenAICompatVideoSubmitHandler(h)
	fetch := tkOpenAICompatVideoFetchHandler(h)
	g.POST("/video/generations", submit)
	g.GET("/video/generations/:task_id", fetch)
	g.POST("/videos", submit)
	g.GET("/videos/:task_id", fetch)
}

// registerTKVideoRoutesNoPrefix mirrors registerTKVideoRoutes for the legacy
// no-`/v1`-prefix aliases that the rest of the gateway exposes for the same
// endpoint family. The middleware list is passed in by the caller because
// gin.Engine.METHOD does not accept a pre-built chain.
func registerTKVideoRoutesNoPrefix(r *gin.Engine, h *handler.Handlers, mw ...gin.HandlerFunc) {
	submit := tkOpenAICompatVideoSubmitHandler(h)
	fetch := tkOpenAICompatVideoFetchHandler(h)
	r.POST("/video/generations", append(mw, submit)...)
	r.GET("/video/generations/:task_id", append(mw, fetch)...)
	r.POST("/videos", append(mw, submit)...)
	r.GET("/videos/:task_id", append(mw, fetch)...)
}
