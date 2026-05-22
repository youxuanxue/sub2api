//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

// setClaudeStatusForTest stores a snapshot into the package-global atom and
// restores a benign operational snapshot afterwards so the global state does
// not leak into other tests in the package.
func setClaudeStatusForTest(t *testing.T, snap ClaudeStatusSnapshot) {
	t.Helper()
	claudeStatusAtom.Store(&snap)
	t.Cleanup(func() {
		claudeStatusAtom.Store(&ClaudeStatusSnapshot{Status: "operational", FetchedAt: time.Now()})
	})
}

// During a live Claude API incident the 3/3 counter still advances (so ops
// retains the failure signal) but the ladder MUST NOT write
// SetTempUnschedulable — the error is Anthropic's fault, not the account's.
func TestRateLimitService_AnthropicUpstreamError_IncidentSkipsCooldownWrite(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{
		IsIncident: true,
		Status:     "partial_outage",
		FetchedAt:  time.Now(),
	})

	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1, 2, 3},
		tierCounts: []int64{1},
	}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{ID: 801, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	for i := 0; i < 3; i++ {
		shouldDisable := svc.HandleUpstreamError(
			context.Background(),
			account,
			http.StatusInternalServerError,
			http.Header{},
			[]byte(`{"error":{"type":"api_error","message":"upstream 500"}}`),
		)
		if i < 2 {
			require.False(t, shouldDisable, "hit %d below threshold should not disable", i+1)
		} else {
			require.True(t, shouldDisable, "third hit still returns shouldDisable for failover")
		}
	}

	require.Equal(t, 0, repo.tempCalls,
		"incident in progress: ladder MUST suppress SetTempUnschedulable so account health is not penalised")
	require.Equal(t, []int64{801, 801, 801}, counter.incrementIDs,
		"3/3 counter advances on every hit even during an incident (ops signal preserved)")
}

// Control: with Claude API operational the same 3/3 sequence DOES land the
// ladder cooldown write — proving the incident gate, not some other change,
// drives the suppression above.
func TestRateLimitService_AnthropicUpstreamError_OperationalStillLadders(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{
		IsIncident: false,
		Status:     "operational",
		FetchedAt:  time.Now(),
	})

	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1, 2, 3},
		tierCounts: []int64{1},
	}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{ID: 802, Platform: PlatformAnthropic, Type: AccountTypeOAuth}

	for i := 0; i < 3; i++ {
		svc.HandleUpstreamError(
			context.Background(),
			account,
			http.StatusInternalServerError,
			http.Header{},
			[]byte(`{"error":{"type":"api_error","message":"upstream 500"}}`),
		)
	}

	require.Equal(t, 1, repo.tempCalls,
		"operational status: third hit MUST land the ladder cooldown (normal protection)")
}

// A stale incident snapshot (status page unreachable for too long) must fail
// safe to non-incident so cooldown writes are not suppressed forever.
func TestRateLimitService_AnthropicUpstreamError_StaleIncidentFailsSafe(t *testing.T) {
	setClaudeStatusForTest(t, ClaudeStatusSnapshot{
		IsIncident: true,
		Status:     "partial_outage",
		FetchedAt:  time.Now().Add(-claudeStatusMaxStaleness - time.Minute),
	})

	require.False(t, IsClaudeAPIIncident(),
		"an incident reading older than the staleness bound must be treated as resolved")

	repo := &rateLimitAccountRepoStub{}
	counter := &anthropicUpstreamErrorCounterCacheStub{
		counts:     []int64{1, 2, 3},
		tierCounts: []int64{1},
	}
	svc := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	svc.SetAnthropicUpstreamErrorCounterCache(counter)

	account := &Account{ID: 803, Platform: PlatformAnthropic, Type: AccountTypeOAuth}
	for i := 0; i < 3; i++ {
		svc.HandleUpstreamError(
			context.Background(),
			account,
			http.StatusInternalServerError,
			http.Header{},
			[]byte(`{"error":{"type":"api_error","message":"upstream 500"}}`),
		)
	}

	require.Equal(t, 1, repo.tempCalls,
		"stale incident snapshot must not suppress the cooldown write")
}

func TestFetchClaudeAPIStatus(t *testing.T) {
	cases := []struct {
		name         string
		body         string
		wantStatus   string
		wantIncident bool
	}{
		{
			name:         "operational",
			body:         `{"components":[{"id":"k8w3r06qmzrp","status":"operational"}]}`,
			wantStatus:   "operational",
			wantIncident: false,
		},
		{
			name:         "partial_outage is an incident",
			body:         `{"components":[{"id":"k8w3r06qmzrp","status":"partial_outage"}]}`,
			wantStatus:   "partial_outage",
			wantIncident: true,
		},
		{
			name:         "degraded_performance is an incident (conservative)",
			body:         `{"components":[{"id":"k8w3r06qmzrp","status":"degraded_performance"}]}`,
			wantStatus:   "degraded_performance",
			wantIncident: true,
		},
		{
			name:         "Claude API component absent leaves status unknown, not an incident",
			body:         `{"components":[{"id":"some_other_component","status":"major_outage"}]}`,
			wantStatus:   "unknown",
			wantIncident: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			snap, err := fetchClaudeAPIStatus(context.Background(), srv.Client(), srv.URL)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, snap.Status)
			require.Equal(t, tc.wantIncident, snap.IsIncident)
			require.False(t, snap.FetchedAt.IsZero())
		})
	}
}

func TestFetchClaudeAPIStatus_BadJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`not json`))
	}))
	defer srv.Close()

	_, err := fetchClaudeAPIStatus(context.Background(), srv.Client(), srv.URL)
	require.Error(t, err)
}
