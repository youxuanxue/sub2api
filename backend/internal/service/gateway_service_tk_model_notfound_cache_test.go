package service

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestTkModelNotFoundCacheKey(t *testing.T) {
	// lower + trim normalization so case/whitespace variants share an entry
	require.Equal(t,
		tkModelNotFoundCacheKey(PlatformAnthropic, "Claude-Haiku-4-6"),
		tkModelNotFoundCacheKey(PlatformAnthropic, "  claude-haiku-4-6 "))
	// empty model -> "" (never a hittable key)
	require.Equal(t, "", tkModelNotFoundCacheKey(PlatformAnthropic, "   "))
	require.Equal(t, "", tkModelNotFoundCacheKey(PlatformAnthropic, ""))
	// platform isolation: same model name under different platforms must not collide
	require.NotEqual(t,
		tkModelNotFoundCacheKey(PlatformAnthropic, "claude-haiku-4-6"),
		tkModelNotFoundCacheKey(PlatformOpenAI, "claude-haiku-4-6"))
}

func TestTkModelNotFoundCacheGetPutTTL(t *testing.T) {
	cache := &tkModelNotFoundNegativeCache{}

	// miss on empty cache
	require.False(t, cache.get(PlatformAnthropic, "claude-haiku-4-6"))
	// empty model never stored / never hit
	cache.put(PlatformAnthropic, "")
	require.False(t, cache.get(PlatformAnthropic, ""))

	// put -> hit
	cache.put(PlatformAnthropic, "claude-haiku-4-6")
	require.True(t, cache.get(PlatformAnthropic, "claude-haiku-4-6"))

	// force-expire the entry and confirm get() returns false AND lazily evicts it
	key := tkModelNotFoundCacheKey(PlatformAnthropic, "claude-haiku-4-6")
	cache.m.Store(key, time.Now().Add(-time.Second))
	require.False(t, cache.get(PlatformAnthropic, "claude-haiku-4-6"))
	_, stillThere := cache.m.Load(key)
	require.False(t, stillThere, "expired entry must be evicted on read")

	// nil-receiver safe
	var nilCache *tkModelNotFoundNegativeCache
	require.False(t, nilCache.get(PlatformAnthropic, "claude-haiku-4-6"))
	require.NotPanics(t, func() { nilCache.put(PlatformAnthropic, "claude-haiku-4-6") })
}

func newGinTestCtx() (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	return c, w
}

func TestTkModelNotFoundShortCircuit_Hit(t *testing.T) {
	cache := &tkModelNotFoundNegativeCache{}
	cache.put(PlatformAnthropic, "claude-haiku-4-6")
	s := &GatewayService{tkModelNotFoundCache: cache}
	acct := &Account{ID: 1, Platform: PlatformAnthropic}
	c, w := newGinTestCtx()

	handled, err := s.tkModelNotFoundShortCircuit(c, acct, "claude-haiku-4-6")

	require.True(t, handled)
	require.Error(t, err)
	require.Equal(t, http.StatusBadRequest, w.Code)
	body := w.Body.String()
	require.Contains(t, body, TkUnsupportedModelErrType)
	require.Contains(t, body, TkUnsupportedModelMessage("claude-haiku-4-6"))
}

func TestTkModelNotFoundShortCircuit_Miss(t *testing.T) {
	s := &GatewayService{tkModelNotFoundCache: &tkModelNotFoundNegativeCache{}}
	acct := &Account{ID: 1, Platform: PlatformAnthropic}
	c, w := newGinTestCtx()

	handled, err := s.tkModelNotFoundShortCircuit(c, acct, "claude-haiku-4-6")

	require.False(t, handled)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, w.Code) // recorder default, nothing written
	require.Empty(t, w.Body.String())
}

// The cache keys on the POST-mapping model name. A request whose name an account
// remaps to a valid model (claude-haiku-4-6 -> claude-haiku-4-5) reaches the gate
// with the mapped (valid) name, which is never the cached not-found key — so it
// must NOT be short-circuited even while the typo name is cached.
func TestTkModelNotFoundShortCircuit_MappedNameNotShortCircuited(t *testing.T) {
	cache := &tkModelNotFoundNegativeCache{}
	cache.put(PlatformAnthropic, "claude-haiku-4-6") // the passthrough typo got 404'd
	s := &GatewayService{tkModelNotFoundCache: cache}
	acct := &Account{ID: 2, Platform: PlatformAnthropic}
	c, w := newGinTestCtx()

	// gate is called with the MAPPED valid name
	handled, err := s.tkModelNotFoundShortCircuit(c, acct, "claude-haiku-4-5")

	require.False(t, handled)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestTkModelNotFoundShortCircuit_PlatformScoped(t *testing.T) {
	cache := &tkModelNotFoundNegativeCache{}
	cache.put(PlatformAnthropic, "claude-haiku-4-6")
	s := &GatewayService{tkModelNotFoundCache: cache}
	// non-Anthropic account must never be gated, even with a populated cache
	acct := &Account{ID: 3, Platform: PlatformOpenAI}
	c, w := newGinTestCtx()

	handled, err := s.tkModelNotFoundShortCircuit(c, acct, "claude-haiku-4-6")

	require.False(t, handled)
	require.NoError(t, err)
	require.Equal(t, http.StatusOK, w.Code)
}

func TestTkModelNotFoundShortCircuit_NilSafety(t *testing.T) {
	acct := &Account{ID: 1, Platform: PlatformAnthropic}

	// nil cache field
	s := &GatewayService{}
	c, _ := newGinTestCtx()
	handled, err := s.tkModelNotFoundShortCircuit(c, acct, "claude-haiku-4-6")
	require.False(t, handled)
	require.NoError(t, err)

	// nil gin context
	s2 := &GatewayService{tkModelNotFoundCache: &tkModelNotFoundNegativeCache{}}
	handled, err = s2.tkModelNotFoundShortCircuit(nil, acct, "claude-haiku-4-6")
	require.False(t, handled)
	require.NoError(t, err)

	// nil account
	c2, _ := newGinTestCtx()
	handled, err = s2.tkModelNotFoundShortCircuit(c2, nil, "claude-haiku-4-6")
	require.False(t, handled)
	require.NoError(t, err)
}

func TestTkModelNotFoundRecordUpstream404(t *testing.T) {
	cache := &tkModelNotFoundNegativeCache{}
	s := &GatewayService{tkModelNotFoundCache: cache}

	// records an Anthropic not-found so a later gate hits
	s.tkModelNotFoundRecordUpstream404(PlatformAnthropic, "claude-haiku-4-6")
	require.True(t, cache.get(PlatformAnthropic, "claude-haiku-4-6"))

	// non-Anthropic platform is a no-op (out of scope)
	s.tkModelNotFoundRecordUpstream404(PlatformOpenAI, "some-bad-model")
	require.False(t, cache.get(PlatformOpenAI, "some-bad-model"))

	// empty model is a no-op
	s.tkModelNotFoundRecordUpstream404(PlatformAnthropic, "   ")
	require.False(t, cache.get(PlatformAnthropic, "   "))
}
