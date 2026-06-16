package service

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
)

// TokenKey: reusable, operator-editable "试用方案" (trial presets).
//
// A trial preset is the named, reusable bundle behind the Invite-to-Trial flow:
// group (which gates models + windowed quota) + validity_days (到期时间) +
// initial balance (充值金额) + per-group rate override (倍率) + concurrency/RPM.
// It is the first-class, operator-facing promotion of the previously-hidden
// `default_subscriptions` cold-start setting — see docs/approved/user-cold-start.md
// and the Invite-to-Trial plan.
//
// Stored as a single JSON settings row (mirrors `default_subscriptions`), so no
// ent schema change is needed. Unset → empty list (graceful), so we deliberately
// do NOT seed the upstream defaults map.

// SettingKeyTrialPresets is the settings key holding the JSON trial-preset list.
// Declared here (not in the upstream-shaped domain_constants.go) to keep this a
// pure TK-only addition.
const SettingKeyTrialPresets = "trial_presets"

// TrialPreset is one reusable Invite-to-Trial configuration.
//
// GroupID MUST reference a subscription-type group: the trial's expiry is modeled
// as a group subscription (user_subscriptions.expires_at), and AssignSubscription
// rejects non-subscription groups. Validation enforces this on write.
type TrialPreset struct {
	Name         string  `json:"name"`
	GroupID      int64   `json:"group_id"`
	ValidityDays int     `json:"validity_days"`
	Balance      float64 `json:"balance"`
	Concurrency  int     `json:"concurrency"`
	RPMLimit     int     `json:"rpm_limit"`
	// Rate is the per-group rate (倍率) override applied to provisioned users.
	// nil → inherit the group's rate_multiplier (no per-user override).
	Rate *float64 `json:"rate,omitempty"`
}

// GetTrialPresets returns the configured trial presets (nil when unset/invalid).
func (s *SettingService) GetTrialPresets(ctx context.Context) []TrialPreset {
	value, err := s.settingRepo.GetValue(ctx, SettingKeyTrialPresets)
	if err != nil {
		return nil
	}
	return parseTrialPresets(value)
}

// SetTrialPresets validates and persists the trial-preset list.
func (s *SettingService) SetTrialPresets(ctx context.Context, presets []TrialPreset) error {
	if presets == nil {
		presets = []TrialPreset{}
	}
	normalized, err := s.validateTrialPresets(ctx, presets)
	if err != nil {
		return err
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return infraerrors.BadRequest("INVALID_TRIAL_PRESETS", "failed to encode trial presets")
	}
	if err := s.settingRepo.Set(ctx, SettingKeyTrialPresets, string(data)); err != nil {
		return err
	}
	if s.onUpdate != nil {
		s.onUpdate()
	}
	return nil
}

// parseTrialPresets decodes the raw JSON, dropping structurally-invalid rows.
// Mirrors parseDefaultSubscriptions: silent on decode error (returns nil) so a
// corrupted row never breaks reads.
func parseTrialPresets(raw string) []TrialPreset {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	var items []TrialPreset
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil
	}
	normalized := make([]TrialPreset, 0, len(items))
	for _, item := range items {
		if strings.TrimSpace(item.Name) == "" || item.GroupID <= 0 {
			continue
		}
		normalized = append(normalized, normalizeTrialPreset(item))
	}
	return normalized
}

// normalizeTrialPreset clamps/sanitizes a single preset's numeric fields.
func normalizeTrialPreset(item TrialPreset) TrialPreset {
	item.Name = strings.TrimSpace(item.Name)
	if item.ValidityDays <= 0 {
		item.ValidityDays = 30
	}
	if item.ValidityDays > MaxValidityDays {
		item.ValidityDays = MaxValidityDays
	}
	if item.Balance < 0 {
		item.Balance = 0
	}
	if item.Concurrency < 0 {
		item.Concurrency = 0
	}
	if item.RPMLimit < 0 {
		item.RPMLimit = 0
	}
	if item.Rate != nil && *item.Rate < 0 {
		item.Rate = nil
	}
	return item
}

// validateTrialPresets enforces unique names and subscription-type groups.
// Mirrors validateDefaultSubscriptionGroups, reusing defaultSubGroupReader.
func (s *SettingService) validateTrialPresets(ctx context.Context, presets []TrialPreset) ([]TrialPreset, error) {
	normalized := make([]TrialPreset, 0, len(presets))
	seenNames := make(map[string]struct{}, len(presets))
	for _, item := range presets {
		name := strings.TrimSpace(item.Name)
		if name == "" {
			return nil, infraerrors.BadRequest("INVALID_TRIAL_PRESET_NAME", "trial preset name must not be empty")
		}
		if _, dup := seenNames[name]; dup {
			return nil, infraerrors.BadRequest("DUPLICATE_TRIAL_PRESET_NAME", "trial preset name must be unique: "+name)
		}
		seenNames[name] = struct{}{}

		if item.GroupID <= 0 {
			return nil, infraerrors.BadRequest("INVALID_TRIAL_PRESET_GROUP", "trial preset must reference a group")
		}
		if s.defaultSubGroupReader != nil {
			group, err := s.defaultSubGroupReader.GetByID(ctx, item.GroupID)
			if err != nil {
				return nil, infraerrors.BadRequest("INVALID_TRIAL_PRESET_GROUP",
					"trial preset group not found: "+strconv.FormatInt(item.GroupID, 10))
			}
			if !group.IsSubscriptionType() {
				return nil, infraerrors.BadRequest("INVALID_TRIAL_PRESET_GROUP",
					"trial preset group must be a subscription-type group: "+strconv.FormatInt(item.GroupID, 10))
			}
		}
		normalized = append(normalized, normalizeTrialPreset(item))
	}
	return normalized, nil
}
