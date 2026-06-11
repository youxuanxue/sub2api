//go:build unit

package service

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

const tkUsageCredits429Body = `{"type":"error","error":{"type":"rate_limit_error","message":"Usage credits are required for long context requests."}}`

func TestTkIsAnthropicRequestOwned429Message(t *testing.T) {
	// Marker in the parsed message.
	require.True(t, tkIsAnthropicRequestOwned429Message("Usage credits are required for long context requests.", nil))
	// Case-insensitive.
	require.True(t, tkIsAnthropicRequestOwned429Message("USAGE CREDITS ARE REQUIRED for long context requests.", nil))
	// Marker only in the raw body.
	require.True(t, tkIsAnthropicRequestOwned429Message("", []byte(tkUsageCredits429Body)))
	// Genuine account-level rate limits must NOT match.
	require.False(t, tkIsAnthropicRequestOwned429Message("Number of request tokens has exceeded your per-minute rate limit", nil))
	require.False(t, tkIsAnthropicRequestOwned429Message("", []byte(`{"type":"error","error":{"type":"rate_limit_error","message":"Rate limited"}}`)))
	require.False(t, tkIsAnthropicRequestOwned429Message("", nil))
}

func newRequestOwned429TestContext(t *testing.T) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader("{}"))
	return c, rec
}

func anthropic429Response(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

// Incident 2026-06-11: the deterministic policy 429 must be short-circuited on
// the FIRST hop — original status + body passed through (so the prod mirror hop
// can re-classify the same phrase), and the returned error must NOT be a
// failover error.
func TestTkHandleAnthropicRequestOwned429_KnownPhraseShortCircuitsFirstHop(t *testing.T) {
	c, rec := newRequestOwned429TestContext(t)
	svc := &GatewayService{}
	account := &Account{ID: 1, Name: "cc-us3", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}

	result, err, handled := svc.tkHandleAnthropicRequestOwned429(c, account, anthropic429Response(tkUsageCredits429Body), []byte(tkUsageCredits429Body))
	require.True(t, handled)
	require.Nil(t, result)
	require.Error(t, err)
	var failoverErr *UpstreamFailoverError
	require.False(t, errors.As(err, &failoverErr), "request-owned 429 must not fan out")

	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "Usage credits are required", "original policy phrase must survive the mirror boundary")
}

// Unknown deterministic 429s: identical message text from N accounts within one
// request trips the breaker at the Nth occurrence; the first N-1 hops keep
// normal failover semantics.
func TestTkHandleAnthropicRequestOwned429_SameTextBreaker(t *testing.T) {
	c, rec := newRequestOwned429TestContext(t)
	svc := &GatewayService{}
	body := `{"type":"error","error":{"type":"rate_limit_error","message":"Some future deterministic rejection"}}`

	for i := 1; i < tkAnthropic429SameTextThreshold; i++ {
		account := &Account{ID: int64(i), Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
		_, _, handled := svc.tkHandleAnthropicRequestOwned429(c, account, anthropic429Response(body), []byte(body))
		require.False(t, handled, "occurrence %d must keep normal failover semantics", i)
	}

	last := &Account{ID: 99, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	_, err, handled := svc.tkHandleAnthropicRequestOwned429(c, last, anthropic429Response(body), []byte(body))
	require.True(t, handled, "occurrence %d (identical text) must trip the breaker", tkAnthropic429SameTextThreshold)
	require.Error(t, err)
	require.Equal(t, http.StatusTooManyRequests, rec.Code)
	require.Contains(t, rec.Body.String(), "Some future deterministic rejection")
}

// Distinct texts (genuine per-account rate limits) must never trip the breaker.
func TestTkHandleAnthropicRequestOwned429_DistinctTextsNeverTrip(t *testing.T) {
	c, _ := newRequestOwned429TestContext(t)
	svc := &GatewayService{}
	texts := []string{"limit A", "limit B", "limit C", "limit D"}
	for i, msg := range texts {
		body := `{"type":"error","error":{"type":"rate_limit_error","message":"` + msg + `"}}`
		account := &Account{ID: int64(i + 1), Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
		_, _, handled := svc.tkHandleAnthropicRequestOwned429(c, account, anthropic429Response(body), []byte(body))
		require.False(t, handled, "distinct text %q must not trip the breaker", msg)
	}
}

func TestTkHandleAnthropicRequestOwned429_ScopeGuards(t *testing.T) {
	c, _ := newRequestOwned429TestContext(t)
	svc := &GatewayService{}

	// Non-anthropic platform is out of scope even with the policy phrase.
	openaiAccount := &Account{ID: 7, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	_, _, handled := svc.tkHandleAnthropicRequestOwned429(c, openaiAccount, anthropic429Response(tkUsageCredits429Body), []byte(tkUsageCredits429Body))
	require.False(t, handled)

	// Non-429 statuses are out of scope.
	anthropicAccount := &Account{ID: 8, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	resp503 := anthropic429Response(tkUsageCredits429Body)
	resp503.StatusCode = http.StatusServiceUnavailable
	_, _, handled = svc.tkHandleAnthropicRequestOwned429(c, anthropicAccount, resp503, []byte(tkUsageCredits429Body))
	require.False(t, handled)

	// Nil gin context (no request scope to track) must be a no-op.
	_, _, handled = svc.tkHandleAnthropicRequestOwned429(nil, anthropicAccount, anthropic429Response(tkUsageCredits429Body), []byte(tkUsageCredits429Body))
	require.False(t, handled)
}
