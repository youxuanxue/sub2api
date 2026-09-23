package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// requestRetentionDays reads the authoritative policy immediately before deletion.
// Missing legacy settings use the deployment default; unreadable or invalid
// settings stop cleanup instead of silently shortening the retention window.
func (s *OpsCleanupService) requestRetentionDays(ctx context.Context) (int, error) {
	days := UsageLogRetentionDays(s.cfg)
	if s.settingRepo == nil {
		return days, nil
	}
	raw, err := s.settingRepo.GetValue(ctx, SettingKeyOpsRuntimeLogConfig)
	if errors.Is(err, ErrSettingNotFound) {
		return days, nil
	}
	if err != nil {
		return 0, err
	}
	var value struct {
		RequestRetentionDays *int `json:"request_retention_days"`
	}
	if err := json.Unmarshal([]byte(raw), &value); err != nil {
		return 0, err
	}
	if value.RequestRetentionDays != nil {
		days = *value.RequestRetentionDays
	}
	if days < 1 || days > prodUsageLogRetentionDays {
		return 0, fmt.Errorf("invalid request_retention_days: %d (expected 1–90)", days)
	}
	return days, nil
}
