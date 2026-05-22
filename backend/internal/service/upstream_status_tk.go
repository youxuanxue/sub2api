package service

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	claudeStatusAPIURL       = "https://status.claude.com/api/v2/components.json"
	claudeAPIComponentID     = "k8w3r06qmzrp" // "Claude API (api.anthropic.com)"
	claudeStatusPollInterval = 30 * time.Second
	claudeStatusFetchTimeout = 5 * time.Second
)

// ClaudeStatusSnapshot is the most recent status polled from status.claude.com.
type ClaudeStatusSnapshot struct {
	IsIncident bool   // true when Claude API is not operational
	Status     string // "operational" | "degraded_performance" | "partial_outage" | "major_outage"
	FetchedAt  time.Time
}

var claudeStatusAtom atomic.Value // stores *ClaudeStatusSnapshot

// GetClaudeStatusSnapshot returns the last known Claude API status.
// Returns a zero-value snapshot (IsIncident=false) until the first successful poll.
func GetClaudeStatusSnapshot() ClaudeStatusSnapshot {
	if v := claudeStatusAtom.Load(); v != nil {
		return *v.(*ClaudeStatusSnapshot)
	}
	return ClaudeStatusSnapshot{}
}

// IsClaudeAPIIncident reports whether the most recent Claude API status is non-operational.
func IsClaudeAPIIncident() bool {
	return GetClaudeStatusSnapshot().IsIncident
}

// StartClaudeStatusPoller starts a background goroutine that polls status.claude.com
// every 30 seconds and updates the package-level snapshot. Exits when ctx is cancelled.
// Safe to call multiple times (each call spawns a goroutine; call once from main).
func StartClaudeStatusPoller(ctx context.Context) {
	client := &http.Client{Timeout: claudeStatusFetchTimeout}

	// Prime the snapshot synchronously so the very first requests benefit too.
	if snap, err := fetchClaudeAPIStatus(ctx, client); err == nil {
		claudeStatusAtom.Store(snap)
	}

	go func() {
		ticker := time.NewTicker(claudeStatusPollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				snap, err := fetchClaudeAPIStatus(ctx, client)
				if err != nil {
					slog.Warn("claude_status_poll_failed", "error", err)
					continue
				}
				prev := GetClaudeStatusSnapshot()
				claudeStatusAtom.Store(snap)
				if snap.IsIncident && !prev.IsIncident {
					slog.Warn("claude_api_incident_detected",
						"status", snap.Status,
						"effect", "anthropic_account_cooldown_writes_suppressed")
				} else if !snap.IsIncident && prev.IsIncident {
					slog.Info("claude_api_incident_resolved", "status", snap.Status)
				}
			}
		}
	}()
}

type claudeComponentsResponse struct {
	Components []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"components"`
}

func fetchClaudeAPIStatus(ctx context.Context, client *http.Client) (*ClaudeStatusSnapshot, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, claudeStatusAPIURL, nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if err != nil {
		return nil, err
	}

	var cr claudeComponentsResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return nil, err
	}

	snap := &ClaudeStatusSnapshot{FetchedAt: time.Now(), Status: "unknown"}
	for _, c := range cr.Components {
		if c.ID == claudeAPIComponentID {
			snap.Status = c.Status
			snap.IsIncident = c.Status != "operational"
			break
		}
	}
	return snap, nil
}
