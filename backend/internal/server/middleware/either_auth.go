package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// EitherAuthMiddleware accepts a request authenticated by EITHER user-scope
// JWT (browser / 用户中心 session) OR user-scope API key (sk-...). Both
// branches end up writing the same AuthSubject{UserID, Concurrency} into
// the gin context, so handlers downstream do not need to know which
// branch took it.
//
// Shipped for issue #63: M0's dual-CC pipeline (and any other SDK / CI
// caller of TokenKey) holds an API key, not a JWT — TokenKey's notion of
// "developer credential" is the API key. The QA self-export endpoint
// MUST therefore accept both, otherwise the same Bearer token that
// authenticates `POST /v1/messages` (200) gets rejected by
// `POST /api/v1/users/me/qa/export` (401), which is the bug #63
// describes verbatim.
//
// Dispatch is by token shape on the Authorization header:
//   - "Bearer eyJ..." (or any value with two dots and the JWT base64-url
//     prefix) → JWT branch
//   - everything else, including missing Authorization header but
//     present x-api-key / x-goog-api-key headers → API-key branch
//
// This shape check is deliberately simple — the underlying middlewares
// still do full validation; the dispatcher just routes to the correct
// validator. Wrong-shape tokens will fail in their respective middleware
// with the same 401 they always would have.
type EitherAuthMiddleware gin.HandlerFunc

// NewEitherAuthMiddleware composes the existing JWT + API-key middlewares
// behind a single dispatcher. Both branches retain their full validation
// (TokenVersion, IP whitelist, user status, etc.) — we only choose which
// one to invoke based on the credential shape on the wire.
func NewEitherAuthMiddleware(jwtAuth JWTAuthMiddleware, apiKeyAuth APIKeyAuthMiddleware) EitherAuthMiddleware {
	jwtFn := gin.HandlerFunc(jwtAuth)
	apiKeyFn := gin.HandlerFunc(apiKeyAuth)
	return EitherAuthMiddleware(func(c *gin.Context) {
		if looksLikeJWTRequest(c) {
			jwtFn(c)
			return
		}
		apiKeyFn(c)
	})
}

// looksLikeJWTRequest returns true iff the Authorization header carries
// a JWS Compact Serialization token (three base64url segments separated
// by dots). All TokenKey JWTs are HS256 → header `{"alg":"HS256",...}`
// → base64url prefix `eyJ`. API keys (sk-..., admin-...) are flat
// strings without dots, so the test is unambiguous.
func looksLikeJWTRequest(c *gin.Context) bool {
	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		return false
	}
	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
		return false
	}
	token := strings.TrimSpace(parts[1])
	return looksLikeJWT(token)
}

// looksLikeJWT is exported for unit tests. Token is a JWT iff it starts
// with the standard `eyJ` JWS header prefix AND contains exactly two
// dots (header.payload.signature). The two-dot check rejects API keys
// that happen to start with `eyJ` by coincidence; the prefix check
// rejects unrelated three-dotted strings.
func looksLikeJWT(token string) bool {
	if !strings.HasPrefix(token, "eyJ") {
		return false
	}
	return strings.Count(token, ".") == 2
}
