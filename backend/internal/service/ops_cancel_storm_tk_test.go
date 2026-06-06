//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// minimal SettingRepository stub for exercising loadConfig (only GetValue matters).
type cancelStormSettingRepo struct {
	val string
	err error
}

func (r *cancelStormSettingRepo) Get(context.Context, string) (*Setting, error)         { return nil, nil }
func (r *cancelStormSettingRepo) GetValue(context.Context, string) (string, error)       { return r.val, r.err }
func (r *cancelStormSettingRepo) Set(context.Context, string, string) error              { return nil }
func (r *cancelStormSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return nil, nil
}
func (r *cancelStormSettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (r *cancelStormSettingRepo) GetAll(context.Context) (map[string]string, error)    { return nil, nil }
func (r *cancelStormSettingRepo) Delete(context.Context, string) error                 { return nil }

func newTestCancelStormDetector(cfg *CancelStormConfig, doer opsFeishuHTTPDoer, now time.Time) *cancelStormDetector {
	d := newCancelStormDetector(nil, &fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, "edge-test")
	if doer != nil {
		d.httpClient = doer
	}
	d.now = func() time.Time { return now }
	normalizeCancelStormConfig(cfg)
	d.cachedCfg = cfg
	// Pin the config cache well beyond any time the test advances to, so loadConfig
	// (with a nil settingRepo) is never reached and the injected cfg is authoritative.
	d.cfgFetchedAt = now.Add(time.Hour)
	return d
}

func waitForFeishuCalls(t *testing.T, doer *blockingFeishuDoer, want int) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		if doer.callCount() >= want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timeout waiting for %d feishu calls, got %d", want, doer.callCount())
		case <-time.After(2 * time.Millisecond):
		}
	}
}

func TestCancelStormLoadConfig(t *testing.T) {
	t.Run("valid json parsed and clamped", func(t *testing.T) {
		repo := &cancelStormSettingRepo{val: `{"mode":"detect_only","window_seconds":120,"min_sample_count":30,"cancel_rate_threshold":0.7,"alert_cooldown_seconds":300,"opus_only":true}`}
		d := newCancelStormDetector(repo, nil, "edge-test")
		cfg := d.loadConfig()
		require.Equal(t, cancelStormModeDetectOnly, cfg.Mode)
		require.Equal(t, 120, cfg.WindowSeconds)
		require.Equal(t, 30, cfg.MinSampleCount)
		require.InDelta(t, 0.7, cfg.CancelRateThreshold, 1e-9)
		require.True(t, cfg.OpusOnly)
	})
	t.Run("missing row falls back to default off", func(t *testing.T) {
		repo := &cancelStormSettingRepo{err: ErrSettingNotFound}
		cfg := newCancelStormDetector(repo, nil, "x").loadConfig()
		require.Equal(t, cancelStormModeOff, cfg.Mode)
	})
	t.Run("corrupt json falls back to default off", func(t *testing.T) {
		repo := &cancelStormSettingRepo{val: `{not json`}
		cfg := newCancelStormDetector(repo, nil, "x").loadConfig()
		require.Equal(t, cancelStormModeOff, cfg.Mode)
	})
	t.Run("unknown mode normalizes to off", func(t *testing.T) {
		repo := &cancelStormSettingRepo{val: `{"mode":"enforce"}`}
		cfg := newCancelStormDetector(repo, nil, "x").loadConfig()
		require.Equal(t, cancelStormModeOff, cfg.Mode, "enforce not implemented in Phase 1 -> off")
	})
}

func TestCancelStormModeOffNoAlert(t *testing.T) {
	now := time.Unix(1700000000, 0)
	doer := &blockingFeishuDoer{}
	d := newTestCancelStormDetector(&CancelStormConfig{Mode: cancelStormModeOff, WindowSeconds: 60, MinSampleCount: 2}, doer, now)
	for i := 0; i < 10; i++ {
		d.observe(7, "borrowed-key", "claude-opus-4-8", true)
	}
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, 0, doer.callCount(), "mode=off must never alert")
	require.Empty(t, d.states, "mode=off must not even count")
}

func TestCancelStormBelowMinSampleNoAlert(t *testing.T) {
	now := time.Unix(1700000000, 0)
	doer := &blockingFeishuDoer{}
	d := newTestCancelStormDetector(&CancelStormConfig{Mode: cancelStormModeDetectOnly, WindowSeconds: 60, MinSampleCount: 5, CancelRateThreshold: 0.5}, doer, now)
	// 4 requests all canceled: rate 1.0 but below the 5-sample floor.
	for i := 0; i < 4; i++ {
		d.observe(7, "k", "claude-opus-4-8", true)
	}
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, 0, doer.callCount())
}

func TestCancelStormCrossThresholdAlerts(t *testing.T) {
	now := time.Unix(1700000000, 0)
	doer := &blockingFeishuDoer{done: make(chan struct{}, 4)}
	d := newTestCancelStormDetector(&CancelStormConfig{Mode: cancelStormModeDetectOnly, WindowSeconds: 60, MinSampleCount: 5, CancelRateThreshold: 0.5, AlertCooldownSeconds: 600}, doer, now)
	// 5 requests, 3 canceled -> rate 0.6 >= 0.5 and total 5 >= 5 -> alert.
	pattern := []bool{true, true, true, false, false}
	for _, c := range pattern {
		d.observe(7, "borrowed-key", "claude-opus-4-8", c)
	}
	waitForFeishuCalls(t, doer, 1)
	body := doer.lastBody()
	require.Contains(t, body, "borrowed-key (#7)")
	require.Contains(t, body, "60%")
}

func TestCancelStormDedupWithinCooldown(t *testing.T) {
	now := time.Unix(1700000000, 0)
	doer := &blockingFeishuDoer{done: make(chan struct{}, 8)}
	d := newTestCancelStormDetector(&CancelStormConfig{Mode: cancelStormModeDetectOnly, WindowSeconds: 600, MinSampleCount: 2, CancelRateThreshold: 0.5, AlertCooldownSeconds: 600}, doer, now)
	for i := 0; i < 10; i++ {
		d.observe(7, "k", "claude-opus-4-8", true)
	}
	waitForFeishuCalls(t, doer, 1)
	time.Sleep(40 * time.Millisecond)
	require.Equal(t, 1, doer.callCount(), "within cooldown only one alert per key")
}

func TestCancelStormOpusOnlyGatesNonOpus(t *testing.T) {
	now := time.Unix(1700000000, 0)
	doer := &blockingFeishuDoer{}
	d := newTestCancelStormDetector(&CancelStormConfig{Mode: cancelStormModeDetectOnly, WindowSeconds: 60, MinSampleCount: 2, CancelRateThreshold: 0.5, OpusOnly: true}, doer, now)
	for i := 0; i < 10; i++ {
		d.observe(7, "k", "claude-sonnet-4-6", true)
	}
	time.Sleep(30 * time.Millisecond)
	require.Equal(t, 0, doer.callCount(), "opus_only must skip non-opus models")
	require.Empty(t, d.states)
}

func TestCancelStormWindowTumblingResets(t *testing.T) {
	now := time.Unix(1700000000, 0)
	cur := now
	d := newCancelStormDetector(nil, &fakeIncidentConfigProvider{cfg: enabledFeishuConfig()}, "edge-test")
	d.now = func() time.Time { return cur }
	d.httpClient = &blockingFeishuDoer{}
	cfg := &CancelStormConfig{Mode: cancelStormModeDetectOnly, WindowSeconds: 60, MinSampleCount: 100, CancelRateThreshold: 0.5}
	normalizeCancelStormConfig(cfg)
	d.cachedCfg = cfg
	d.cfgFetchedAt = now.Add(time.Hour)

	// Fill the first window with cancels (min_sample high so no alert).
	for i := 0; i < 3; i++ {
		d.observe(7, "k", "claude-opus-4-8", true)
	}
	d.mu.Lock()
	require.Equal(t, 3, d.states[7].total)
	require.Equal(t, 3, d.states[7].canceled)
	d.mu.Unlock()

	// Advance past the window; the next observe must reset, not accumulate.
	cur = now.Add(61 * time.Second)
	d.observe(7, "k", "claude-opus-4-8", false)
	d.mu.Lock()
	require.Equal(t, 1, d.states[7].total, "window must tumble-reset total")
	require.Equal(t, 0, d.states[7].canceled, "old-window cancels must not carry over")
	require.Equal(t, cur, d.states[7].windowStart)
	d.mu.Unlock()
}

func TestCancelStormConfigNormalizeClamps(t *testing.T) {
	c := &CancelStormConfig{Mode: "DETECT_ONLY", WindowSeconds: -5, MinSampleCount: 0, CancelRateThreshold: 9, AlertCooldownSeconds: -1}
	normalizeCancelStormConfig(c)
	require.Equal(t, cancelStormModeDetectOnly, c.Mode)
	require.Equal(t, 60, c.WindowSeconds)
	require.Equal(t, 1, c.MinSampleCount)
	require.InDelta(t, 0.5, c.CancelRateThreshold, 1e-9)
	require.Equal(t, 0, c.AlertCooldownSeconds)
}
