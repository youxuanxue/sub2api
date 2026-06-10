package service

// TK: per-API-key "cancel-storm" detector — Phase 1 (detect + Feishu alert only).
//
// Why: an external automation client borrowing a TokenKey API key with short
// client timeouts produced a context-canceled flood on opus relay traffic — the
// "non-human harness" traffic shape that trips Anthropic's abuse filter and got
// an irreplaceable subscription-OAuth account org-disabled. TokenKey had no
// real-time signal (the incident was only reconstructed post-mortem). This counts
// per-key cancel rate at the gateway terminal-outcome chokepoint
// (OpsErrorLoggerMiddleware) and fires one Feishu card when a key crosses the
// threshold, so operators see the storm as it happens.
//
// Scope: detect + alert ONLY. Auto-throttle (reducing the offending key's
// concurrency) is intentionally a separate later PR — these thresholds must first
// be calibrated against real traffic under detect_only before any enforcement.
//
// Counting is in-memory per process (tumbling window). TokenKey Stage0 runs a
// single gateway container per node, so per-process counting is exact for the
// shape this guards; if a node ever scales horizontally, move the counters to
// Redis (the Phase 2 enforcement work introduces that substrate). The alert path
// reuses the account-incident Feishu primitives (feishuCardPayload /
// sendFeishuPayload) so the card shape + signing live in one place.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const (
	cancelStormModeOff        = "off"
	cancelStormModeDetectOnly = "detect_only"

	// cancelStormConfigTTL bounds how often the hot path reads the config setting
	// from the DB. The common production state is mode=off, so this keeps the
	// gateway path from doing a settings read on every request.
	cancelStormConfigTTL = 10 * time.Second

	// cancelStormMaxTrackedKeys caps the in-memory state map; beyond it idle
	// entries are swept. A volume floor far above any realistic distinct-key count.
	cancelStormMaxTrackedKeys = 4096
)

// CancelStormConfig is the JSON config stored under SettingKeyCancelStormConfig.
// Phase 1 only: mode is "off" | "detect_only".
type CancelStormConfig struct {
	Mode                 string  `json:"mode"`
	WindowSeconds        int     `json:"window_seconds"`
	MinSampleCount       int     `json:"min_sample_count"`
	CancelRateThreshold  float64 `json:"cancel_rate_threshold"`
	MinCancelCount       int     `json:"min_cancel_count"`
	AlertCooldownSeconds int     `json:"alert_cooldown_seconds"`
	OpusOnly             bool    `json:"opus_only"`
}

func defaultCancelStormConfig() *CancelStormConfig {
	return &CancelStormConfig{
		Mode:                 cancelStormModeOff,
		WindowSeconds:        60,
		MinSampleCount:       20,
		CancelRateThreshold:  0.5,
		MinCancelCount:       0,
		AlertCooldownSeconds: 600,
		OpusOnly:             false,
	}
}

// normalizeCancelStormConfig clamps values into safe ranges and forces an
// unknown/typo'd mode to "off" (fail-safe: a misconfigured row never spams).
func normalizeCancelStormConfig(c *CancelStormConfig) {
	if c == nil {
		return
	}
	if strings.TrimSpace(strings.ToLower(c.Mode)) == cancelStormModeDetectOnly {
		c.Mode = cancelStormModeDetectOnly
	} else {
		c.Mode = cancelStormModeOff
	}
	if c.WindowSeconds <= 0 {
		c.WindowSeconds = 60
	}
	if c.WindowSeconds > 3600 {
		c.WindowSeconds = 3600
	}
	if c.MinSampleCount < 1 {
		c.MinSampleCount = 1
	}
	if c.CancelRateThreshold <= 0 || c.CancelRateThreshold > 1 {
		c.CancelRateThreshold = 0.5
	}
	if c.MinCancelCount < 0 {
		c.MinCancelCount = 0
	}
	if c.AlertCooldownSeconds < 0 {
		c.AlertCooldownSeconds = 0
	}
}

type cancelStormWindow struct {
	windowStart time.Time
	total       int
	canceled    int
	lastAlertAt time.Time
	lastSeen    time.Time
}

type cancelStormDetector struct {
	settingRepo SettingRepository
	cfgProvider opsFeishuConfigProvider
	httpClient  opsFeishuHTTPDoer
	siteID      string
	now         func() time.Time

	cfgMu        sync.Mutex
	cachedCfg    *CancelStormConfig
	cfgFetchedAt time.Time

	mu          sync.Mutex
	states      map[int64]*cancelStormWindow
	lastSweepAt time.Time
}

func newCancelStormDetector(settingRepo SettingRepository, cfgProvider opsFeishuConfigProvider, siteID string) *cancelStormDetector {
	site := strings.TrimSpace(siteID)
	if site == "" {
		site = "unknown"
	}
	return &cancelStormDetector{
		settingRepo: settingRepo,
		cfgProvider: cfgProvider,
		httpClient:  &http.Client{Timeout: opsFeishuWebhookTimeout},
		siteID:      site,
		now:         time.Now,
		states:      map[int64]*cancelStormWindow{},
	}
}

func (d *cancelStormDetector) currentTime() time.Time {
	if d != nil && d.now != nil {
		return d.now()
	}
	return time.Now()
}

// config returns the parsed config, cached for cancelStormConfigTTL so the hot
// path does not hit the settings store on every request.
func (d *cancelStormDetector) config() *CancelStormConfig {
	now := d.currentTime()
	d.cfgMu.Lock()
	if d.cachedCfg != nil && now.Sub(d.cfgFetchedAt) < cancelStormConfigTTL {
		cfg := d.cachedCfg
		d.cfgMu.Unlock()
		return cfg
	}
	d.cfgMu.Unlock()

	cfg := d.loadConfig()

	d.cfgMu.Lock()
	d.cachedCfg = cfg
	d.cfgFetchedAt = now
	d.cfgMu.Unlock()
	return cfg
}

func (d *cancelStormDetector) loadConfig() *CancelStormConfig {
	def := defaultCancelStormConfig()
	if d.settingRepo == nil {
		return def
	}
	raw, err := d.settingRepo.GetValue(context.Background(), SettingKeyCancelStormConfig)
	if err != nil {
		// Missing row (migration not yet applied) or transient error -> safe
		// default (off). Never block or error the gateway path.
		return def
	}
	cfg := &CancelStormConfig{}
	if jErr := json.Unmarshal([]byte(raw), cfg); jErr != nil {
		return def
	}
	normalizeCancelStormConfig(cfg)
	return cfg
}

// observe feeds one terminal request outcome into the per-key window and fires an
// alert when the cancel rate crosses the configured threshold. No-op unless mode
// is "detect_only".
func (d *cancelStormDetector) observe(apiKeyID int64, apiKeyName, model string, canceled bool) {
	if d == nil || apiKeyID <= 0 {
		return
	}
	cfg := d.config()
	if cfg.Mode != cancelStormModeDetectOnly {
		return
	}
	// OpusOnly watches the expensive tier for client-cancel abuse. Fable is the
	// tier above Opus (priciest of all), so it belongs in scope too — otherwise
	// the costliest model would silently escape the costly-model watch.
	if cfg.OpusOnly && !isOpusModel(model) && !isFableModel(model) {
		return
	}

	now := d.currentTime()
	window := time.Duration(cfg.WindowSeconds) * time.Second

	d.mu.Lock()
	w := d.states[apiKeyID]
	if w == nil {
		w = &cancelStormWindow{windowStart: now}
		d.states[apiKeyID] = w
	}
	if now.Sub(w.windowStart) >= window {
		// Tumbling reset; lastAlertAt is preserved across windows so the alert
		// cooldown spans windows (otherwise every fresh window could re-alert).
		w.windowStart = now
		w.total = 0
		w.canceled = 0
	}
	w.total++
	if canceled {
		w.canceled++
	}
	w.lastSeen = now

	shouldAlert := false
	var rate float64
	var total, cancels int
	if w.total >= cfg.MinSampleCount {
		rate = float64(w.canceled) / float64(w.total)
		crossed := rate >= cfg.CancelRateThreshold && w.canceled >= cfg.MinCancelCount
		cooldownOK := w.lastAlertAt.IsZero() || now.Sub(w.lastAlertAt) >= time.Duration(cfg.AlertCooldownSeconds)*time.Second
		if crossed && cooldownOK {
			w.lastAlertAt = now
			shouldAlert = true
			total, cancels = w.total, w.canceled
		}
	}
	d.sweepLocked(now, window)
	d.mu.Unlock()

	if shouldAlert {
		go d.sendAlert(apiKeyID, apiKeyName, model, rate, total, cancels, cfg)
	}
}

// sweepLocked bounds memory by dropping keys idle beyond a few windows. It only
// engages once the map grows past the cap AND at most once per window, so the
// O(n) scan is amortized and never runs on the per-request hot path under d.mu in
// steady state. Caller holds d.mu.
func (d *cancelStormDetector) sweepLocked(now time.Time, window time.Duration) {
	if len(d.states) < cancelStormMaxTrackedKeys {
		return
	}
	if !d.lastSweepAt.IsZero() && now.Sub(d.lastSweepAt) < window {
		return
	}
	d.lastSweepAt = now
	cutoff := 4 * window
	for id, w := range d.states {
		if now.Sub(w.lastSeen) > cutoff {
			delete(d.states, id)
		}
	}
}

func (d *cancelStormDetector) sendAlert(apiKeyID int64, apiKeyName, model string, rate float64, total, canceled int, cfg *CancelStormConfig) {
	defer func() {
		if r := recover(); r != nil {
			logger.LegacyPrintf("service.cancel_storm", "[CancelStorm] alert panic recovered: %v", r)
		}
	}()
	if d == nil || d.cfgProvider == nil {
		return
	}
	fcfg, err := d.cfgProvider.GetEmailNotificationConfig(context.Background())
	if err != nil || fcfg == nil || !fcfg.Feishu.Enabled || strings.TrimSpace(fcfg.Feishu.WebhookURL) == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), opsFeishuWebhookTimeout)
	defer cancel()

	title := "TokenKey 取消风暴"
	if d.siteID != "" && d.siteID != "unknown" {
		title = title + " · " + d.siteID
	}
	body := buildCancelStormText(d.siteID, apiKeyID, apiKeyName, model, rate, total, canceled, cfg, d.currentTime())
	payload := feishuCardPayload(fcfg.Feishu, d.now, "orange", title, body)
	if sErr := sendFeishuPayload(ctx, d.httpClient, fcfg.Feishu, payload); sErr != nil {
		logger.LegacyPrintf("service.cancel_storm", "[CancelStorm] feishu send failed: %s", sErr.Error())
	}
}

func buildCancelStormText(site string, apiKeyID int64, apiKeyName, model string, rate float64, total, canceled int, cfg *CancelStormConfig, now time.Time) string {
	keyLabel := fmt.Sprintf("#%d", apiKeyID)
	if name := strings.TrimSpace(apiKeyName); name != "" {
		keyLabel = fmt.Sprintf("%s (#%d)", name, apiKeyID)
	}
	modelLabel := strings.TrimSpace(model)
	if modelLabel == "" {
		modelLabel = "-"
	}
	advice := "疑似外部客户端以短超时/程序化方式滥用该 key,持续高取消率会触发上游(Anthropic)滥用风控并可能封禁承载账号。建议核查该 key 来源,必要时降并发/限流或暂停该 key。"
	return fmt.Sprintf("**节点**：%s\n**API Key**：%s\n**模型**：%s\n**取消率**：%.0f%%（%d/%d，窗口 %ds）\n**模式**：%s\n**时间**：%s\n\n**建议**：%s",
		escapeFeishuText(site),
		escapeFeishuText(keyLabel),
		escapeFeishuText(modelLabel),
		rate*100, canceled, total, cfg.WindowSeconds,
		escapeFeishuText(cfg.Mode),
		escapeFeishuText(formatAlertTime(now)),
		advice,
	)
}

func isOpusModel(model string) bool {
	return strings.Contains(strings.ToLower(model), "opus")
}

// ObserveCancelStorm feeds one terminal request outcome (api key id + name, model,
// whether the client canceled) into the per-key cancel-storm detector. No-op
// unless cancel_storm_config mode is "detect_only". Safe on nil receiver / detector.
func (s *OpsService) ObserveCancelStorm(apiKeyID int64, apiKeyName, model string, canceled bool) {
	if s == nil || s.cancelStorm == nil {
		return
	}
	s.cancelStorm.observe(apiKeyID, apiKeyName, model, canceled)
}
