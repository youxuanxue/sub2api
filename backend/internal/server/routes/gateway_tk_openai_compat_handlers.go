package routes

import (
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// isOpenAICompatPlatform delegates to the canonical service-layer helper so
// that adding a sixth compat platform only requires updating
// service.OpenAICompatPlatforms(). Kept as a thin local wrapper to keep the
// route-table call sites concise.
func isOpenAICompatPlatform(platform string) bool {
	return service.IsOpenAICompatPlatform(platform)
}

func tkOpenAICompatMessagesPOST(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isOpenAICompatPlatform(getGroupPlatform(c)) {
			h.OpenAIGateway.Messages(c)
			return
		}
		h.Gateway.Messages(c)
	}
}

func tkOpenAICompatCountTokensPOST(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isOpenAICompatPlatform(getGroupPlatform(c)) {
			h.OpenAIGateway.CountTokens(c)
			return
		}
		h.Gateway.CountTokens(c)
	}
}

func tkOpenAICompatResponsesPOST(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isOpenAICompatPlatform(getGroupPlatform(c)) {
			h.OpenAIGateway.Responses(c)
			return
		}
		h.Gateway.Responses(c)
	}
}

func tkOpenAICompatResponsesWebSocketGET(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		h.OpenAIGateway.ResponsesWebSocket(c)
	}
}

func tkOpenAICompatChatCompletionsPOST(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if isOpenAICompatPlatform(getGroupPlatform(c)) {
			h.OpenAIGateway.ChatCompletions(c)
			return
		}
		h.Gateway.ChatCompletions(c)
	}
}

func isNativeOpenAIMediaPlatform(platform string) bool {
	return platform == service.PlatformOpenAI
}

func isGrokNativeVideoGenerationRoute(c *gin.Context) bool {
	switch c.FullPath() {
	case "/v1/videos/generations", "/videos/generations":
		return true
	default:
		return false
	}
}

func isGrokNativeVideoStatusRoute(c *gin.Context) bool {
	switch c.FullPath() {
	case "/v1/videos/:task_id", "/videos/:task_id", "/v1/videos/:request_id", "/videos/:request_id":
		return true
	default:
		return false
	}
}

// tkOpenAICompatEmbeddingsHandler routes POST /embeddings for OpenAI-compat (incl. newapi) platform groups only.
func tkOpenAICompatEmbeddingsHandler(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !isOpenAICompatPlatform(getGroupPlatform(c)) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "The embeddings API is only available for OpenAI-compatible platform groups",
				},
			})
			return
		}
		h.OpenAIGateway.Embeddings(c)
	}
}

// tkOpenAICompatImageGenerationsHandler routes POST /images/generations for OpenAI-compat platform groups only.
func tkOpenAICompatImageGenerationsHandler(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch getGroupPlatform(c) {
		case service.PlatformGrok:
			h.OpenAIGateway.GrokImages(c)
			return
		case service.PlatformOpenAI:
			h.OpenAIGateway.ImageGenerations(c)
			return
		}
		if !isOpenAICompatPlatform(getGroupPlatform(c)) {
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "The images API is only available for OpenAI-compatible platform groups",
				},
			})
			return
		}
		h.OpenAIGateway.ImageGenerations(c)
	}
}

// tkOpenAICompatImageEditsHandler routes POST /images/edits for native OpenAI
// and Grok groups. NewAPI bridge channels do not currently expose a uniform
// edit capability, so they stay gated here.
func tkOpenAICompatImageEditsHandler(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		switch getGroupPlatform(c) {
		case service.PlatformOpenAI:
			h.OpenAIGateway.Images(c)
			return
		case service.PlatformGrok:
			h.OpenAIGateway.GrokImages(c)
			return
		}
		if !isNativeOpenAIMediaPlatform(getGroupPlatform(c)) {
			service.MarkOpsClientBusinessLimited(c, service.OpsClientBusinessLimitedReasonLocalFeatureGate)
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "The image edits API is only available for OpenAI platform groups",
				},
			})
			return
		}
		h.OpenAIGateway.Images(c)
	}
}

// tkOpenAICompatVideoSubmitHandler routes POST /video/generations and POST
// /videos for OpenAI-compat (incl. newapi) platform groups only. The async
// video task pipeline is bridge-only (no native sub2api implementation), so
// non-compat groups always 404 here regardless of the underlying platform's
// capabilities. Inlined for consistency with the embeddings / images handlers
// directly above.
func tkOpenAICompatVideoSubmitHandler(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformGrok && isGrokNativeVideoGenerationRoute(c) {
			h.OpenAIGateway.GrokVideoGeneration(c)
			return
		}
		if !isOpenAICompatPlatform(getGroupPlatform(c)) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "The video generation API is only available for OpenAI-compatible platform groups",
				},
			})
			return
		}
		h.OpenAIGateway.VideoSubmit(c)
	}
}

// tkOpenAICompatVideoFetchHandler routes GET /video/generations/:task_id and
// GET /videos/:task_id for OpenAI-compat platform groups. The platform check
// is on the caller's API key group, NOT on the task's originating platform —
// since `openai` and `newapi` are both OpenAI-compatible, a key that switches
// between them within the compat class can still poll. Cross-class polling
// (e.g. anthropic key polling a newapi task) returns 404 here.
func tkOpenAICompatVideoFetchHandler(h *handler.Handlers) gin.HandlerFunc {
	return func(c *gin.Context) {
		if getGroupPlatform(c) == service.PlatformGrok && isGrokNativeVideoStatusRoute(c) {
			h.OpenAIGateway.GrokVideoStatus(c)
			return
		}
		if !isOpenAICompatPlatform(getGroupPlatform(c)) {
			c.JSON(http.StatusNotFound, gin.H{
				"error": gin.H{
					"type":    "invalid_request_error",
					"message": "The video generation API is only available for OpenAI-compatible platform groups",
				},
			})
			return
		}
		h.OpenAIGateway.VideoFetch(c)
	}
}

// registerTKOpenAICompatVideoRoutes wires the async video task routes: POST
// submit at `/video/generations`, the OpenAI-compat alias `/videos`, and the
// xAI-shaped alias `/videos/generations` (needed for the prod→edge grok relay,
// see below); GET fetch at `/video/generations/:task_id` and `/videos/:task_id`.
// Called once per scope from gateway.go so the upstream-shape file holds a
// single helper invocation per scope instead of inline route registrations +
// comments — keeps `gateway.go` close to upstream and makes "add another video
// alias" a single-file change here. The two scopes behave identically;
// gateway.go passes its own pre-built middleware chain for the no-prefix scope.
//
// Supported channel types are auto-derived from `relay.GetTaskAdaptor`
// (currently 45 = VolcEngine / Doubao Seedance, 54 = DoubaoVideo); adding a
// new task adapter upstream lights up automatically — no route changes here.
func registerTKOpenAICompatVideoRoutes(group *gin.RouterGroup, h *handler.Handlers) {
	submit := tkOpenAICompatVideoSubmitHandler(h)
	fetch := tkOpenAICompatVideoFetchHandler(h)
	group.POST("/video/generations", submit)
	group.GET("/video/generations/:task_id", fetch)
	group.POST("/videos", submit)
	group.GET("/videos/:task_id", fetch)
	// `/videos/generations` is xAI's exact video-submit path shape (grok's
	// native arm POSTs `{base}/videos/generations`). Registering it makes the
	// gateway accept that shape too, which is REQUIRED for the prod→edge grok
	// relay: a prod grok mirror account (platform=grok) forwards a video submit
	// to `api-<edge>.tokenkey.dev/v1/videos/generations`, and the edge gateway
	// must route it (grok chat/image relay works only because their xAI paths —
	// /chat/completions, /images/generations — already coincide with gateway
	// routes; the video path did not). Same submit handler; GET fetch already
	// matches `/videos/:task_id`, so no fetch alias is needed.
	group.POST("/videos/generations", submit)
}

// registerTKOpenAICompatVideoRoutesNoPrefix mirrors the above for the
// no-/v1-prefix aliases registered directly on *gin.Engine. Same handler
// pair, same middleware chain as the sibling unprefixed routes
// (chat/completions, embeddings, images/generations).
func registerTKOpenAICompatVideoRoutesNoPrefix(r *gin.Engine, h *handler.Handlers, mw ...gin.HandlerFunc) {
	submit := tkOpenAICompatVideoSubmitHandler(h)
	fetch := tkOpenAICompatVideoFetchHandler(h)
	chain := func(handler gin.HandlerFunc) []gin.HandlerFunc {
		out := make([]gin.HandlerFunc, 0, len(mw)+1)
		out = append(out, mw...)
		out = append(out, handler)
		return out
	}
	r.POST("/video/generations", chain(submit)...)
	r.GET("/video/generations/:task_id", chain(fetch)...)
	r.POST("/videos", chain(submit)...)
	r.GET("/videos/:task_id", chain(fetch)...)
	// xAI-shaped submit alias — see registerTKOpenAICompatVideoRoutes (required
	// for the prod→edge grok video relay).
	r.POST("/videos/generations", chain(submit)...)
}
