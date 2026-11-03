package service

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"github.com/Wei-Shaw/sub2api/internal/pkg/googleapi"
	"github.com/gin-gonic/gin"
)

// googleGeneric403RequestKey marks a gateway request that may see the same
// Gemini / Antigravity generic 403 on more than one account. The value is
// request-scoped mutable state, not a process-wide counter.
type googleGeneric403RequestKey struct{}

type googleGeneric403Request struct {
	mu       sync.Mutex
	accounts map[int64]struct{}
	stop     bool
}

// bindGoogleGeneric403Request installs the two-strike tracker on a gateway
// request. Background jobs and admin probes call HandleUpstreamError without a
// gin request, so they keep today's permanent disable. The tracker is stored on
// the gin request context so later account switches in the same request share it.
func bindGoogleGeneric403Request(c *gin.Context, ctx context.Context) context.Context {
	if c == nil || c.Request == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if googleGeneric403RequestFrom(ctx) != nil {
		return ctx
	}
	if parent := c.Request.Context(); parent != nil {
		if tracker := googleGeneric403RequestFrom(parent); tracker != nil {
			return context.WithValue(ctx, googleGeneric403RequestKey{}, tracker)
		}
	}
	tracker := &googleGeneric403Request{accounts: make(map[int64]struct{})}
	ctx = context.WithValue(ctx, googleGeneric403RequestKey{}, tracker)
	base := c.Request.Context()
	if base == nil {
		base = context.Background()
	}
	c.Request = c.Request.WithContext(context.WithValue(base, googleGeneric403RequestKey{}, tracker))
	return ctx
}

func withGoogleGeneric403Request(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if googleGeneric403RequestFrom(ctx) != nil {
		return ctx
	}
	return context.WithValue(ctx, googleGeneric403RequestKey{}, &googleGeneric403Request{
		accounts: make(map[int64]struct{}),
	})
}

func googleGeneric403RequestFrom(ctx context.Context) *googleGeneric403Request {
	if ctx == nil {
		return nil
	}
	tracker, _ := ctx.Value(googleGeneric403RequestKey{}).(*googleGeneric403Request)
	return tracker
}

func googleGeneric403RequestActive(ctx context.Context) bool {
	return googleGeneric403RequestFrom(ctx) != nil
}

// noteGoogleGeneric403 records one generic 403 for this request.
// The first distinct account may fail over. A second distinct account stops
// further account switches. Repeating the same account does not count as the
// second strike.
func noteGoogleGeneric403(ctx context.Context, accountID int64) (allowFailover bool) {
	state := googleGeneric403RequestFrom(ctx)
	if state == nil {
		return true
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.accounts == nil {
		state.accounts = make(map[int64]struct{})
	}
	if state.stop {
		return false
	}
	if _, seen := state.accounts[accountID]; seen {
		return true
	}
	state.accounts[accountID] = struct{}{}
	if len(state.accounts) >= 2 {
		state.stop = true
		return false
	}
	return true
}

func googleGeneric403StopsFailover(ctx context.Context) bool {
	state := googleGeneric403RequestFrom(ctx)
	if state == nil {
		return false
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	return state.stop
}

// tryGoogleGeneric403RequestScope applies the request-level two-strike rule to
// PlatformGemini and PlatformAntigravity generic 403s. generic is the default
// of classifyForbiddenType. Validation, violation, and account-owned 403s that
// the repo already recognizes stay on their existing owners.
// handled=false means the caller must keep today's permanent disable.
func (s *RateLimitService) tryGoogleGeneric403RequestScope(
	ctx context.Context,
	account *Account,
	responseBody []byte,
) (handled bool, shouldDisable bool) {
	if s == nil || account == nil {
		return false, false
	}
	if account.Platform != PlatformGemini && account.Platform != PlatformAntigravity {
		return false, false
	}
	if !googleGeneric403RequestActive(ctx) {
		return false, false
	}
	if classifyForbiddenType(string(responseBody)) != forbiddenTypeForbidden {
		return false, false
	}
	if isGoogleAccountOwnedForbidden(responseBody) {
		return false, false
	}
	allowFailover := noteGoogleGeneric403(ctx, account.ID)
	slog.Info("google_generic_403_request_scoped",
		"account_id", account.ID,
		"platform", account.Platform,
		"allow_failover", allowFailover,
	)
	return true, allowFailover
}

// isGoogleAccountOwnedForbidden reports 403s the repo already treats as an
// account or credential fault rather than a request-scoped generic denial.
// Cached content, consumer suspension, project denial, insufficient scope,
// SERVICE_DISABLED, and IAM service-account identities stay on the permanent
// path (or their existing early owner).
func isGoogleAccountOwnedForbidden(body []byte) bool {
	if isGeminiCachedContentNotFound(http.StatusForbidden, body) {
		return true
	}
	if googleapi.IsServiceDisabledError(string(body)) {
		return true
	}
	if isGeminiInsufficientScope(nil, body) {
		return true
	}
	lower := strings.ToLower(string(body))
	switch {
	case strings.Contains(lower, "consumer has been suspended"):
		return true
	case strings.Contains(lower, "project has been denied"):
		return true
	case strings.Contains(lower, "iam.gserviceaccount.com"):
		return true
	default:
		return false
	}
}

// writeGoogleGeneric403Passthrough writes the upstream status and body without
// remapping them. A confirmed request-level generic 403 stays 403.
func (s *GeminiMessagesCompatService) writeGoogleGeneric403Passthrough(
	c *gin.Context,
	account *Account,
	status int,
	requestID string,
	body []byte,
) error {
	if status == 0 {
		status = http.StatusForbidden
	}
	upstreamMsg := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractUpstreamErrorMessage(body)))
	detail := ""
	if s != nil {
		detail = s.upstreamErrorDetail(body)
	}
	setOpsUpstreamError(c, status, upstreamMsg, detail)
	if account != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			ProxyID:            opsUpstreamProxyID(account),
			ProxyName:          opsUpstreamProxyName(account),
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: status,
			UpstreamRequestID:  requestID,
			Kind:               "http_error",
			Message:            upstreamMsg,
			Detail:             detail,
		})
	}
	if c != nil {
		MarkResponseCommitted(c)
		c.Data(status, "application/json", body)
	}
	if upstreamMsg == "" {
		return fmt.Errorf("upstream error: %d", status)
	}
	return fmt.Errorf("upstream error: %d message=%s", status, upstreamMsg)
}

// writeAntigravityUpstream403 writes the upstream 403 for a confirmed
// request-level generic denial. The body is the existing sanitized client
// envelope; the HTTP status stays the upstream status.
func (s *AntigravityGatewayService) writeAntigravityUpstream403(
	c *gin.Context,
	account *Account,
	status int,
	requestID string,
	body []byte,
) error {
	if status == 0 {
		status = http.StatusForbidden
	}
	clientBody := buildAntigravityClientErrorBody(status, body)
	message := sanitizeUpstreamErrorMessage(strings.TrimSpace(extractAntigravityErrorMessage(clientBody)))
	detail := ""
	if s != nil {
		detail = s.getUpstreamErrorDetail(body)
	}
	setOpsUpstreamError(c, status, message, detail)
	if account != nil {
		appendOpsUpstreamError(c, OpsUpstreamErrorEvent{
			ProxyID:            opsUpstreamProxyID(account),
			ProxyName:          opsUpstreamProxyName(account),
			Platform:           account.Platform,
			AccountID:          account.ID,
			AccountName:        account.Name,
			UpstreamStatusCode: status,
			UpstreamRequestID:  requestID,
			Kind:               "http_error",
			Message:            message,
			Detail:             detail,
		})
	}
	if c != nil {
		MarkResponseCommitted(c)
		c.Data(status, "application/json", clientBody)
	}
	return fmt.Errorf("antigravity upstream error: %d", status)
}
