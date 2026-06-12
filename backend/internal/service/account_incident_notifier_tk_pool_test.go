//go:build unit

package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNotifyPlatformPoolExhausted_SendsP0AndDedupes(t *testing.T) {
	provider := &fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}
	doer := &blockingFeishuDoer{done: make(chan struct{}, 4)}
	fixedNow := time.Date(2026, 6, 11, 6, 49, 0, 0, time.UTC)
	n := newTestNotifier(provider, doer, fixedNow)

	trigger := &Account{ID: 7, Name: "cc-us7", Platform: PlatformAnthropic, Type: AccountTypeAPIKey}
	until := fixedNow.Add(10 * time.Minute)

	n.NotifyPlatformPoolExhausted(PlatformAnthropic, trigger, until, "temp_unschedulable")
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pool-exhausted P0 card was not sent")
	}
	require.Equal(t, 1, doer.callCount())
	body := doer.lastBody()
	require.Contains(t, body, "平台池全不可调度")
	require.Contains(t, body, PlatformAnthropic)
	require.Contains(t, body, "cc-us7")

	// Cooldown-storm shape: every subsequent account block re-triggers the
	// pool check; only the first card within the dedupe window goes out.
	n.NotifyPlatformPoolExhausted(PlatformAnthropic, trigger, until, "temp_unschedulable")
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 1, doer.callCount(), "duplicate pool-exhausted within dedupe window must be suppressed")

	// A different platform has its own dedupe key.
	n.NotifyPlatformPoolExhausted(PlatformOpenAI, trigger, until, "429")
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("second-platform pool-exhausted card was not sent")
	}
	require.Equal(t, 2, doer.callCount())
}
