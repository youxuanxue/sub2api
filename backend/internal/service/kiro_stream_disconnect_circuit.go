package service

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	defaultKiroStreamDisconnectThreshold  = 2
	defaultKiroStreamDisconnectWindow     = time.Minute
	defaultKiroStreamDisconnectCooldown   = 10 * time.Minute
	defaultKiroStreamDisconnectMaxEntries = 4096
	kiroStreamDisconnectReason            = "kiro_stream_disconnect"
)

type kiroStreamDisconnectCircuitSettings struct {
	failureThreshold int
	failureWindow    time.Duration
	cooldown         time.Duration
	maxEntries       int
}

type kiroStreamDisconnectCircuitEntry struct {
	failureCount int
	windowStart  time.Time
	blockedUntil time.Time
	lastTouched  time.Time
}

// kiroStreamDisconnectCircuit bounds repeated post-output transport failures.
// The observation state is process-local; only a tripped cooldown is persisted
// through the existing account temp-unschedulable owner.
type kiroStreamDisconnectCircuit struct {
	mu       sync.Mutex
	settings kiroStreamDisconnectCircuitSettings
	entries  map[int64]kiroStreamDisconnectCircuitEntry
}

func newKiroStreamDisconnectCircuit(settings kiroStreamDisconnectCircuitSettings) *kiroStreamDisconnectCircuit {
	if settings.failureThreshold <= 0 {
		settings.failureThreshold = defaultKiroStreamDisconnectThreshold
	}
	if settings.failureWindow <= 0 {
		settings.failureWindow = defaultKiroStreamDisconnectWindow
	}
	if settings.cooldown <= 0 {
		settings.cooldown = defaultKiroStreamDisconnectCooldown
	}
	if settings.maxEntries <= 0 {
		settings.maxEntries = defaultKiroStreamDisconnectMaxEntries
	}
	return &kiroStreamDisconnectCircuit{
		settings: settings,
		entries:  make(map[int64]kiroStreamDisconnectCircuitEntry),
	}
}

func (s *KiroGatewayService) getKiroStreamDisconnectCircuit() *kiroStreamDisconnectCircuit {
	if s == nil {
		return nil
	}
	s.streamCircuitOnce.Do(func() {
		if s.streamCircuit == nil {
			s.streamCircuit = newKiroStreamDisconnectCircuit(kiroStreamDisconnectCircuitSettings{})
		}
	})
	return s.streamCircuit
}

func (c *kiroStreamDisconnectCircuit) recordFailure(accountID int64, now time.Time) (bool, time.Time) {
	if c == nil || accountID <= 0 {
		return false, time.Time{}
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, exists := c.entries[accountID]
	if exists && now.Before(entry.blockedUntil) {
		entry.lastTouched = now
		c.entries[accountID] = entry
		return false, entry.blockedUntil
	}
	if !exists {
		c.ensureCapacityLocked(now)
	}
	if entry.windowStart.IsZero() || now.Before(entry.windowStart) || now.Sub(entry.windowStart) > c.settings.failureWindow {
		entry.failureCount = 0
		entry.windowStart = now
		entry.blockedUntil = time.Time{}
	}
	entry.failureCount++
	entry.lastTouched = now
	tripped := entry.failureCount >= c.settings.failureThreshold
	if tripped {
		entry.blockedUntil = now.Add(c.settings.cooldown)
	}
	c.entries[accountID] = entry
	return tripped, entry.blockedUntil
}

func (c *kiroStreamDisconnectCircuit) recordSuccess(accountID int64) bool {
	if c == nil || accountID <= 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.entries[accountID]; !ok {
		return false
	}
	delete(c.entries, accountID)
	return true
}

func (c *kiroStreamDisconnectCircuit) ensureCapacityLocked(now time.Time) {
	if len(c.entries) < c.settings.maxEntries {
		return
	}
	for accountID, entry := range c.entries {
		staleObservation := entry.blockedUntil.IsZero() && now.Sub(entry.lastTouched) > c.settings.failureWindow
		expiredCooldown := !entry.blockedUntil.IsZero() && !now.Before(entry.blockedUntil)
		if staleObservation || expiredCooldown {
			delete(c.entries, accountID)
		}
	}
	if len(c.entries) < c.settings.maxEntries {
		return
	}
	var oldestAccountID int64
	var oldest time.Time
	for accountID, entry := range c.entries {
		if oldestAccountID == 0 || entry.lastTouched.Before(oldest) {
			oldestAccountID = accountID
			oldest = entry.lastTouched
		}
	}
	if oldestAccountID > 0 {
		delete(c.entries, oldestAccountID)
	}
}

func shouldRecordKiroStreamDisconnect(ctx context.Context, streamErr error) bool {
	if streamErr == nil || errors.Is(streamErr, context.Canceled) || errors.Is(streamErr, context.DeadlineExceeded) {
		return false
	}
	if ctx != nil && ctx.Err() != nil {
		return false
	}
	if errors.Is(streamErr, io.ErrUnexpectedEOF) || errors.Is(streamErr, io.EOF) {
		return true
	}
	var netErr net.Error
	if errors.As(streamErr, &netErr) {
		return true
	}
	message := strings.ToLower(streamErr.Error())
	if strings.Contains(message, "kiro event stream error:") {
		return false
	}
	for _, marker := range []string{
		"unexpected eof",
		"connection reset",
		"connection closed",
		"client connection lost",
		"broken pipe",
		"tls close_notify",
		"stream error",
	} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func (s *KiroGatewayService) recordKiroStreamDisconnect(
	ctx context.Context,
	account *Account,
	streamErr error,
	model string,
	requestID string,
) {
	if s == nil || account == nil || account.Platform != PlatformKiro || !shouldRecordKiroStreamDisconnect(ctx, streamErr) {
		return
	}
	circuit := s.getKiroStreamDisconnectCircuit()
	tripped, until := circuit.recordFailure(account.ID, time.Now())
	if !tripped {
		return
	}

	errorMessage := "Kiro stream disconnected before completion"
	if model = strings.TrimSpace(model); model != "" {
		errorMessage += " for model " + model
	}
	if safeErr := sanitizeStreamError(streamErr); safeErr != "" {
		errorMessage += ": " + safeErr
	}
	errorMessage = truncateTempUnschedMessage([]byte(errorMessage), tempUnschedMessageMaxBytes)
	state := &TempUnschedState{
		UntilUnix:       until.Unix(),
		TriggeredAtUnix: time.Now().Unix(),
		StatusCode:      0,
		MatchedKeyword:  kiroStreamDisconnectReason,
		RuleIndex:       -1,
		ErrorMessage:    errorMessage,
	}
	reason := errorMessage
	if raw, err := json.Marshal(state); err == nil {
		reason = string(raw)
	}
	if s.accountRepo == nil {
		return
	}
	if err := s.accountRepo.SetTempUnschedulable(ctx, account.ID, until, reason); err != nil {
		slog.Warn("kiro_stream_disconnect_set_temp_unschedulable_failed",
			"account_id", account.ID,
			"request_id", requestID,
			"error", err,
		)
		return
	}
	slog.Warn("kiro_stream_disconnect_account_quarantined",
		"account_id", account.ID,
		"request_id", requestID,
		"model", model,
		"until", until.UTC().Format(time.RFC3339),
		"error", sanitizeStreamError(streamErr),
	)
}

func (s *KiroGatewayService) clearKiroStreamDisconnect(account *Account) {
	if s == nil || account == nil || account.Platform != PlatformKiro {
		return
	}
	if circuit := s.getKiroStreamDisconnectCircuit(); circuit != nil {
		circuit.recordSuccess(account.ID)
	}
}
