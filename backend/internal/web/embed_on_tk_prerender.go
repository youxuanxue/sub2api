//go:build embed

package web

import "github.com/gin-gonic/gin"

// tryServeCrawlerPrerender serves SEO/social-bot HTML before SPA fallback.
// TK companion hook — keeps upstream embed_on.go Middleware() to a one-line call site.
func tryServeCrawlerPrerender(c *gin.Context) bool {
	if !isCrawler(c.GetHeader("User-Agent")) {
		return false
	}
	PrerenderMiddleware()(c)
	return c.IsAborted()
}
