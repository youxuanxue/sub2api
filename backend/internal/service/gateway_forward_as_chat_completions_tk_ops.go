package service

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
)

// recordCCUpstreamRequestError persists a local/transport failure onto the gin
// context so ops_error_logs.upstream_errors keeps the sanitized raw error, and
// writes the same string to the process log. The client-facing CC body stays
// generic; this is the evidence channel for hop failures that never produce an
// upstream HTTP status.
//
// TK companion: keep helper out of the upstream-shaped forward file when possible.
func recordCCUpstreamRequestError(c *gin.Context, account *Account, upstreamURL, kind, safeErr string) {
	if account == nil {
		account = &Account{}
	}
	if c != nil {
		setOpsUpstreamError(c, 0, safeErr, "")
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			Platform:    account.Platform,
			AccountID:   account.ID,
			AccountName: account.Name,
			UpstreamURL: safeUpstreamURL(upstreamURL),
			Kind:        kind,
			Message:     safeErr,
		})
	}
	logger.L().Warn("gateway cc upstream request failed",
		zap.Int64("account_id", account.ID),
		zap.String("account_name", account.Name),
		zap.String("kind", kind),
		zap.String("upstream_url", safeUpstreamURL(upstreamURL)),
		zap.String("error", safeErr),
	)
}
