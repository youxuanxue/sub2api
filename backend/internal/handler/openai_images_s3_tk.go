package handler

import (
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// imagePresignRequest is the body of POST /v1/images/presign.
type imagePresignRequest struct {
	Key string `json:"key"`
}

// ImagesPresign re-mints a short-lived presigned GET URL for an already-offloaded
// generated image, given its S3 key. It is the image analog of VideoFetch's S3
// fast path: Studio history is localStorage-persisted, but presigned URLs are
// intentionally short-lived (the prod signer is the EC2 instance role), so a
// reloaded session re-mints fresh links from the stored key WITHOUT re-generating
// (or re-billing) the image.
//
// Security: the key is hard-scoped to the media/images/ prefix and rejects any
// traversal, so this endpoint can only sign generated-image media — never an
// arbitrary object in the bucket.
func (h *OpenAIGatewayHandler) ImagesPresign(c *gin.Context) {
	if h.mediaStore == nil {
		// The route exists but media offload is not configured on this deployment.
		// 503 (not 404) keeps it distinct from route-not-found: the endpoint is real,
		// the optional feature is simply off. A client only reaches here defensively —
		// with offload disabled, images are served inline so no s3_key is ever issued.
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
			"type": "service_unavailable", "message": "media offload is not enabled",
		}})
		return
	}
	var req imagePresignRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request_error", "message": "invalid request body",
		}})
		return
	}
	key := strings.TrimSpace(req.Key)
	// Validate against the SAME prefix the offload upload side stamps keys with
	// (service.MediaImageKeyPrefix) — single source of truth, so the two sides
	// cannot drift and silently break reload re-mint.
	if key == "" || !strings.HasPrefix(key, service.MediaImageKeyPrefix) || strings.Contains(key, "..") {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{
			"type": "invalid_request_error", "message": "invalid media key",
		}})
		return
	}
	url, err := h.mediaStore.PresignURL(c.Request.Context(), key, mediaPresignTTL)
	if err != nil {
		logger.L().Warn("openai_images.presign_failed", zap.String("key", key), zap.Error(err))
		c.JSON(http.StatusBadGateway, gin.H{"error": gin.H{
			"type": "upstream_error", "message": "failed to presign media",
		}})
		return
	}
	c.JSON(http.StatusOK, gin.H{"url": url})
}
