// Package apipath defines canonical inbound / upstream API endpoint paths.
//
// This is a leaf package with zero internal imports, so any layer
// (handler, service, middleware, observability) can import it without
// creating import cycles.
package apipath

const (
	Messages          = "/v1/messages"
	ChatCompletions   = "/v1/chat/completions"
	Embeddings        = "/v1/embeddings"
	Responses         = "/v1/responses"
	ImagesGenerations = "/v1/images/generations"
	ImagesEdits       = "/v1/images/edits"
	VideosGenerations = "/v1/videos/generations"
	Videos            = "/v1/videos"
	GeminiModels      = "/v1beta/models"
	Models            = "/v1/models"
)
