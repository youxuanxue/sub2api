//go:build unit

package service

import (
	"context"
	"sync/atomic"
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
	// anthropic CTA keeps the prod→edge relay remediation.
	require.Contains(t, body, "scan-edge-health.sh")

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
	// openai(gpt) has no edge topology: the CTA must point at adding accounts /
	// checking seats, NOT the anthropic-only edge-health script (would misdirect
	// ops during a gpt incident — 2026-06-17).
	openaiBody := doer.lastBody()
	require.Contains(t, openaiBody, PlatformOpenAI)
	require.Contains(t, openaiBody, "补充可调度账号")
	require.NotContains(t, openaiBody, "scan-edge-health.sh")
}

func TestCheckPoolRecovery_SendsGreenCardPairedWithExhaust(t *testing.T) {
	provider := &fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}
	doer := &blockingFeishuDoer{done: make(chan struct{}, 4)}
	fixedNow := time.Date(2026, 6, 11, 6, 49, 0, 0, time.UTC)
	n := newTestNotifier(provider, doer, fixedNow)

	// Counter is the recovery poll's single source of truth: 0 while exhausted,
	// then flips to >0 once an account becomes schedulable again.
	var schedulable int64
	n.SetPoolSchedulableCounter(func(_ context.Context, _ string) (int, error) {
		return int(atomic.LoadInt64(&schedulable)), nil
	})

	trigger := &Account{ID: 7, Name: "openai-us3", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	until := fixedNow.Add(10 * time.Minute)

	// 1) Pool exhausted → red P0 card, platform tracked as pending-recovery.
	n.NotifyPlatformPoolExhausted(PlatformOpenAI, trigger, until, "429_fallback")
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pool-exhausted P0 card was not sent")
	}
	require.Equal(t, 1, doer.callCount())

	// 2) Still empty → recovery poll must NOT send anything.
	n.checkPoolRecovery()
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 1, doer.callCount(), "recovery card must not fire while pool is still empty")

	// 3) An account comes back → recovery poll sends a green card.
	atomic.StoreInt64(&schedulable, 2)
	n.checkPoolRecovery()
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pool-recovered green card was not sent")
	}
	require.Equal(t, 2, doer.callCount())
	body := doer.lastBody()
	require.Contains(t, body, "平台池已恢复")
	require.Contains(t, body, PlatformOpenAI)
	require.Contains(t, body, "无需继续补号")

	// 4) Recovery is 1:1 with exhaust: platform was removed from the ledger, so a
	// second poll (still recovered) must not re-send.
	n.checkPoolRecovery()
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 2, doer.callCount(), "recovery must not re-fire for an already-recovered platform")
}

func TestCheckPoolRecovery_NoCounterIsNoop(t *testing.T) {
	provider := &fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}
	doer := &blockingFeishuDoer{done: make(chan struct{}, 4)}
	fixedNow := time.Date(2026, 6, 11, 6, 49, 0, 0, time.UTC)
	n := newTestNotifier(provider, doer, fixedNow)

	trigger := &Account{ID: 7, Name: "openai-us3", Platform: PlatformOpenAI, Type: AccountTypeAPIKey}
	n.NotifyPlatformPoolExhausted(PlatformOpenAI, trigger, fixedNow.Add(10*time.Minute), "429_fallback")
	select {
	case <-doer.done:
	case <-time.After(2 * time.Second):
		t.Fatal("pool-exhausted P0 card was not sent")
	}

	// No counter injected (mirrors a notifier built without a RateLimitService):
	// recovery poll degrades to a no-op, never sends.
	n.checkPoolRecovery()
	time.Sleep(100 * time.Millisecond)
	require.Equal(t, 1, doer.callCount(), "recovery poll must be a no-op when no schedulable counter is wired")
}

func TestFormatPoolOutageDuration(t *testing.T) {
	t.Parallel()
	require.Equal(t, "45秒", formatPoolOutageDuration(45*time.Second))
	require.Equal(t, "2分", formatPoolOutageDuration(2*time.Minute))
	require.Equal(t, "2分30秒", formatPoolOutageDuration(2*time.Minute+30*time.Second))
}
