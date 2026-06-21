//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func anthropicAcct(id int64) *Account {
	return &Account{ID: id, Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
}

func acctIDs(accs []*Account) map[int64]bool {
	out := make(map[int64]bool, len(accs))
	for _, a := range accs {
		out[a.ID] = true
	}
	return out
}

// --- predicate ---

func TestIsSustainedlySaturated_Predicate(t *testing.T) {
	min := anthropicSustainedSaturationMinAgeSeconds
	cases := []struct {
		name string
		st   AnthropicSaturationStreak
		want bool
	}{
		{"zero state", AnthropicSaturationStreak{}, false},
		{"first only", AnthropicSaturationStreak{FirstSeenUnix: 1000}, false},
		{"last only", AnthropicSaturationStreak{LastSeenUnix: 1000}, false},
		{"single stray hit (span 0)", AnthropicSaturationStreak{FirstSeenUnix: 1000, LastSeenUnix: 1000}, false},
		// AMPLIFIER REGRESSION GUARD: a short burst (span < min age) is NEVER
		// sustained, so a 3-request edge blip can never be hard-excluded.
		{"young burst (span 5s)", AnthropicSaturationStreak{FirstSeenUnix: 1000, LastSeenUnix: 1005}, false},
		{"just below min age", AnthropicSaturationStreak{FirstSeenUnix: 1000, LastSeenUnix: 1000 + min - 1}, false},
		{"exactly min age", AnthropicSaturationStreak{FirstSeenUnix: 1000, LastSeenUnix: 1000 + min}, true},
		{"well past min age (30m dead edge)", AnthropicSaturationStreak{FirstSeenUnix: 1000, LastSeenUnix: 1000 + 1800}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			require.Equal(t, c.want, tkIsAnthropicStubSustainedlySaturated(c.st))
		})
	}
}

// --- filter ---

func sustainedGW(t *testing.T, streaks map[int64]AnthropicSaturationStreak) *GatewayService {
	t.Helper()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{streaks: streaks})
	return gw
}

// sustained = a streak whose span comfortably exceeds the min age.
func sustainedStreak() AnthropicSaturationStreak {
	return AnthropicSaturationStreak{FirstSeenUnix: 1000, LastSeenUnix: 1000 + anthropicSustainedSaturationMinAgeSeconds + 60}
}

func TestFilterSustained_DropsDeadStubKeepsHealthy(t *testing.T) {
	resetSatCache()
	// cc-us2 (46) sustainedly saturated, cc-us4 (51) healthy (no streak).
	gw := sustainedGW(t, map[int64]AnthropicSaturationStreak{46: sustainedStreak()})
	in := []*Account{anthropicAcct(46), anthropicAcct(51)}
	out := gw.tkFilterSustainedlySaturated(context.Background(), in)
	require.Len(t, out, 1)
	require.Equal(t, int64(51), out[0].ID, "dead cc-us2 dropped, healthy cc-us4 kept")
}

func TestFilterSustained_AllSaturatedLastResortGuard(t *testing.T) {
	resetSatCache()
	// Both sustainedly saturated => guard keeps the full set (never empty the pool).
	gw := sustainedGW(t, map[int64]AnthropicSaturationStreak{
		46: sustainedStreak(),
		51: sustainedStreak(),
	})
	in := []*Account{anthropicAcct(46), anthropicAcct(51)}
	out := gw.tkFilterSustainedlySaturated(context.Background(), in)
	require.Len(t, out, 2, "all-saturated => keep all (last-resort guard)")
	require.True(t, acctIDs(out)[46] && acctIDs(out)[51])
}

func TestFilterSustained_YoungBurstNotExcluded(t *testing.T) {
	resetSatCache()
	// 503-amplifier regression guard: a burst (young span) on cc-us2 must NOT be
	// excluded even with a healthy sibling present.
	gw := sustainedGW(t, map[int64]AnthropicSaturationStreak{
		46: {FirstSeenUnix: 1000, LastSeenUnix: 1003}, // span 3s
	})
	in := []*Account{anthropicAcct(46), anthropicAcct(51)}
	out := gw.tkFilterSustainedlySaturated(context.Background(), in)
	require.Len(t, out, 2, "young burst must not be hard-excluded")
}

func TestFilterSustained_NonAnthropicUntouched(t *testing.T) {
	resetSatCache()
	// A non-anthropic candidate is never consulted/excluded; the sustained
	// anthropic stub is dropped because a healthy candidate (the non-anthropic
	// one) remains.
	gw := sustainedGW(t, map[int64]AnthropicSaturationStreak{46: sustainedStreak()})
	openai := &Account{ID: 99, Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	in := []*Account{anthropicAcct(46), openai}
	out := gw.tkFilterSustainedlySaturated(context.Background(), in)
	require.Len(t, out, 1)
	require.Equal(t, int64(99), out[0].ID)
}

func TestFilterSustained_SingleCandidateUnchanged(t *testing.T) {
	resetSatCache()
	// < 2 candidates => cannot drop without emptying => unchanged even if dead.
	gw := sustainedGW(t, map[int64]AnthropicSaturationStreak{46: sustainedStreak()})
	in := []*Account{anthropicAcct(46)}
	out := gw.tkFilterSustainedlySaturated(context.Background(), in)
	require.Len(t, out, 1)
	require.Equal(t, int64(46), out[0].ID)
}

func TestFilterSustained_KillSwitchOff(t *testing.T) {
	resetSatCache()
	gw := sustainedGW(t, map[int64]AnthropicSaturationStreak{46: sustainedStreak()})
	gw.settingService = NewSettingService(
		&satSettingRepoStub{values: map[string]string{
			SettingKeyAnthropicSaturatedStubDeprioritizeEnabled: "false",
		}},
		&config.Config{},
	)
	in := []*Account{anthropicAcct(46), anthropicAcct(51)}
	out := gw.tkFilterSustainedlySaturated(context.Background(), in)
	require.Len(t, out, 2, "kill-switch off => no exclusion")
	resetSatCache()
}

func TestFilterSustained_NilCacheInert(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{} // no counter wired
	in := []*Account{anthropicAcct(46), anthropicAcct(51)}
	out := gw.tkFilterSustainedlySaturated(context.Background(), in)
	require.Len(t, out, 2, "nil cache => feature inert")
}

func TestFilterSustained_ReadErrorSafe(t *testing.T) {
	resetSatCache()
	gw := &GatewayService{}
	gw.SetAnthropicSaturationCounter(&fakeSaturationCache{streakErr: context.DeadlineExceeded})
	in := []*Account{anthropicAcct(46), anthropicAcct(51)}
	out := gw.tkFilterSustainedlySaturated(context.Background(), in)
	require.Len(t, out, 2, "read error => candidate set untouched (selection never fails on counter)")
}
