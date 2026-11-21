package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"

	"github.com/stretchr/testify/require"
)

func TestMarkOpsErrorLogUserOptOut(t *testing.T) {
	optedOut := &service.APIKey{User: &service.User{ID: 7, QACaptureEnabled: false}}
	capturing := &service.APIKey{User: &service.User{ID: 8, QACaptureEnabled: true}}

	for _, tc := range []struct {
		name   string
		apiKey *service.APIKey
		want   bool
	}{
		{"opted out user is suppressed", optedOut, true},
		{"capturing user is kept", capturing, false},
		// No resolvable user means there is no opt-out to honor; dropping these
		// would blind operators to platform-wide failures and junk traffic.
		{"nil key is kept", nil, false},
		{"key without user is kept", &service.APIKey{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			entry := &service.OpsInsertErrorLogInput{ErrorType: "api_error"}
			markOpsErrorLogUserOptOut(entry, tc.apiKey)
			require.Equal(t, tc.want, entry.SuppressForUserOptOut)
		})
	}
}

// TestMarkOpsErrorLogUserOptOutNeverUnsets guards the one-way contract: the
// stamp is applied at several sites, and a later call for an unattributable
// key must not clear a suppression an earlier call established.
func TestMarkOpsErrorLogUserOptOutNeverUnsets(t *testing.T) {
	entry := &service.OpsInsertErrorLogInput{SuppressForUserOptOut: true}
	markOpsErrorLogUserOptOut(entry, &service.APIKey{User: &service.User{ID: 9, QACaptureEnabled: true}})
	require.True(t, entry.SuppressForUserOptOut, "stamp must not be cleared by a later capturing-user call")

	markOpsErrorLogUserOptOut(entry, nil)
	require.True(t, entry.SuppressForUserOptOut, "stamp must not be cleared by an unattributable call")
}

// TestOpsCoalescedSummaryCarriesUserOptOut covers the leak the coalescer would
// otherwise reopen: the summary outlives the request that produced it, so a
// field-by-field rebuild that drops the stamp would write a suppressed user's
// collapsed tail after the fact.
func TestOpsCoalescedSummaryCarriesUserOptOut(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	suppressed := opsCoalescedSummary(&service.OpsInsertErrorLogInput{
		ErrorType:             "api_error",
		SuppressForUserOptOut: true,
	}, now)
	require.True(t, suppressed.SuppressForUserOptOut, "summary must carry the opt-out")

	kept := opsCoalescedSummary(&service.OpsInsertErrorLogInput{
		ErrorType: "api_error",
	}, now)
	require.False(t, kept.SuppressForUserOptOut)
}

// TestOpsErrorLogOptOutZeroValueHazard documents why suppression keys off an
// explicitly-loaded flag and why fixtures must set it.
//
// service.User.QACaptureEnabled is a bare bool, so its Go zero value (false)
// means "opted out" — the inverse of the ent schema default
// (users.qa_capture_enabled = true). Production is safe because the auth path
// loads the column (api_key_repo selects user.FieldQaCaptureEnabled), but any
// hand-built User defaults to suppressed. This test pins the asymmetry so the
// next reader does not "simplify" the predicate into trusting a zero value,
// and testutil.NewTestUser sets the field for the same reason.
func TestOpsErrorLogOptOutZeroValueHazard(t *testing.T) {
	zeroValueUser := &service.APIKey{User: &service.User{ID: 1}}
	entry := &service.OpsInsertErrorLogInput{}
	markOpsErrorLogUserOptOut(entry, zeroValueUser)
	require.True(t, entry.SuppressForUserOptOut,
		"a zero-value User reads as opted out; production must load qa_capture_enabled, and fixtures must set it")

	loadedUser := &service.APIKey{User: &service.User{ID: 1, QACaptureEnabled: true}}
	loadedEntry := &service.OpsInsertErrorLogInput{}
	markOpsErrorLogUserOptOut(loadedEntry, loadedUser)
	require.False(t, loadedEntry.SuppressForUserOptOut)
}

// TestOpsErrorLoggerMiddlewareSuppressesOptedOutUser is the end-to-end
// assertion the unit tests above cannot make: it drives a real request through
// OpsErrorLoggerMiddleware and asserts nothing is queued for a user who turned
// QA capture off.
//
// It reuses the quota-reject shape on purpose. That is a pre-auth early exit —
// the api key reaches the logger only through the ops fallback key — which is
// the case the opt-out was asked to cover: those requests never reach QA
// capture, so ops_error_logs was their only persisted trace.
//
// Without this test, deleting any single markOpsErrorLogUserOptOut call site
// still compiles and still passes every other test in this file.
func TestOpsErrorLoggerMiddlewareSuppressesOptedOutUser(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 64)
	gin.SetMode(gin.TestMode)

	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	t.Cleanup(swapOpsErrorLogCoalescer(newOpsCoalescer(time.Minute, opsCoalesceCapacity, clock.Now)))

	ops := service.NewOpsService(&ingressRejectOpsRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	optedOut := &service.APIKey{ID: 901, Key: "sk-opted-out", UserID: 77, User: &service.User{ID: 77, QACaptureEnabled: false}}

	w := httptest.NewRecorder()
	quotaRejectRouter(ops, optedOut).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	require.Equal(t, http.StatusTooManyRequests, w.Code, "suppression must not change the client response")

	require.Empty(t, drainOpsErrorLogQueue(t),
		"a user who turned QA capture off must leave no ops_error_logs row, including pre-auth failures")
}

// TestOpsErrorLoggerMiddlewareStillRecordsCapturingUser is the other half of
// the contract: suppression must be scoped to the opted-out user, not a blanket
// mute of the sink.
func TestOpsErrorLoggerMiddlewareStillRecordsCapturingUser(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 64)
	gin.SetMode(gin.TestMode)

	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	t.Cleanup(swapOpsErrorLogCoalescer(newOpsCoalescer(time.Minute, opsCoalesceCapacity, clock.Now)))

	ops := service.NewOpsService(&ingressRejectOpsRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	capturing := &service.APIKey{ID: 902, Key: "sk-capturing", UserID: 78, User: &service.User{ID: 78, QACaptureEnabled: true}}

	w := httptest.NewRecorder()
	quotaRejectRouter(ops, capturing).ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
	require.Equal(t, http.StatusTooManyRequests, w.Code)

	queued := drainOpsErrorLogQueue(t)
	require.Len(t, queued, 1, "a capturing user's pre-auth failure must still be recorded")
	require.Equal(t, int64(78), *queued[0].UserID)
	require.False(t, queued[0].SuppressForUserOptOut)
}

// TestOpsErrorLoggerCoalescedTailSuppressedForOptedOutUser covers the leak the
// coalescer would reopen: its summary is flushed after the request is gone, so
// a storm from an opted-out user must not resurface as a collapsed tail row.
func TestOpsErrorLoggerCoalescedTailSuppressedForOptedOutUser(t *testing.T) {
	setupOpsErrorLogTestQueue(t, 512)
	gin.SetMode(gin.TestMode)

	clock := &fakeClock{now: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}
	coalescer := newOpsCoalescer(time.Minute, opsCoalesceCapacity, clock.Now)
	t.Cleanup(swapOpsErrorLogCoalescer(coalescer))

	ops := service.NewOpsService(&ingressRejectOpsRepo{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	optedOut := &service.APIKey{ID: 903, Key: "sk-opted-out-storm", UserID: 79, User: &service.User{ID: 79, QACaptureEnabled: false}}
	router := quotaRejectRouter(ops, optedOut)

	for i := 0; i < 50; i++ {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/messages", nil))
		clock.Advance(150 * time.Millisecond)
	}
	// Flush the open bucket the way shutdown would, so any carried tail surfaces.
	clock.Advance(2 * time.Minute)
	coalescer.flushClosed(true)

	require.Empty(t, drainOpsErrorLogQueue(t),
		"the collapsed tail must stay suppressed for an opted-out user")
}
