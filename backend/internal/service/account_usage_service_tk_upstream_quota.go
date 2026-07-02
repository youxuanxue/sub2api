package service

import (
	"sort"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
)

func attachUpstreamQuotaForAccount(account *Account, usage *UsageInfo) {
	if account == nil || usage == nil || usage.UpstreamQuota != nil {
		return
	}
	usage.UpstreamQuota = buildUpstreamQuotaForAccount(account, usage)
}

func buildUpstreamQuotaForAccount(account *Account, usage *UsageInfo) *UpstreamQuotaInfo {
	if account == nil || usage == nil {
		return nil
	}
	switch accountUsageWindowAdapterFor(account) {
	case accountUsageWindowAdapterAnthropic:
		return buildWindowUpstreamQuota("anthropic", usage, []quotaProgressSpec{
			{key: "anthropic_5h", label: "5h", window: "5h", progress: usage.FiveHour},
			{key: "anthropic_7d", label: "7d", window: "7d", progress: usage.SevenDay},
			{key: "anthropic_7d_sonnet", label: "7d Sonnet", window: "7d", progress: usage.SevenDaySonnet},
		})
	case accountUsageWindowAdapterOpenAI:
		return buildWindowUpstreamQuota("openai", usage, []quotaProgressSpec{
			{key: "openai_codex_5h", label: "5h", window: "5h", progress: usage.FiveHour},
			{key: "openai_codex_7d", label: "7d", window: "7d", progress: usage.SevenDay},
		})
	case accountUsageWindowAdapterGemini:
		return buildGeminiUpstreamQuota(usage)
	case accountUsageWindowAdapterAntigravity:
		return buildAntigravityUpstreamQuota(usage)
	case accountUsageWindowAdapterKiro:
		return buildKiroUpstreamQuota(usage)
	case accountUsageWindowAdapterLocal:
		return buildLocalAdapterUpstreamQuota(account, usage)
	default:
		return nil
	}
}

type quotaProgressSpec struct {
	key      string
	label    string
	window   string
	progress *UsageProgress
}

func buildWindowUpstreamQuota(provider string, usage *UsageInfo, specs []quotaProgressSpec) *UpstreamQuotaInfo {
	info := baseUpstreamQuota(provider, usage, defaultUsageSource(usage))
	info.State = "unknown"
	for _, spec := range specs {
		d := progressToQuotaDimension(spec.key, spec.label, spec.window, spec.progress)
		if d == nil {
			continue
		}
		info.Dimensions = append(info.Dimensions, *d)
	}
	if len(info.Dimensions) > 0 {
		info.State = "observed"
	}
	return info
}

func buildGeminiUpstreamQuota(usage *UsageInfo) *UpstreamQuotaInfo {
	info := baseUpstreamQuota("gemini", usage, "simulated")
	info.State = "simulated"
	for _, spec := range []quotaProgressSpec{
		{key: "gemini_shared_daily", label: "Shared 1d", window: "1d", progress: usage.GeminiSharedDaily},
		{key: "gemini_pro_daily", label: "Pro 1d", window: "1d", progress: usage.GeminiProDaily},
		{key: "gemini_flash_daily", label: "Flash 1d", window: "1d", progress: usage.GeminiFlashDaily},
		{key: "gemini_shared_minute", label: "Shared 1m", window: "1m", progress: usage.GeminiSharedMinute},
		{key: "gemini_pro_minute", label: "Pro 1m", window: "1m", progress: usage.GeminiProMinute},
		{key: "gemini_flash_minute", label: "Flash 1m", window: "1m", progress: usage.GeminiFlashMinute},
	} {
		d := progressToQuotaDimension(spec.key, spec.label, spec.window, spec.progress)
		if d == nil {
			continue
		}
		d.Unit = "requests"
		info.Dimensions = append(info.Dimensions, *d)
	}
	if len(info.Dimensions) == 0 {
		info.State = "unknown"
	}
	return info
}

func buildAntigravityUpstreamQuota(usage *UsageInfo) *UpstreamQuotaInfo {
	info := baseUpstreamQuota("antigravity", usage, defaultUsageSource(usage))
	info.State = "unknown"
	info.SubscriptionTier = usage.SubscriptionTier
	info.SubscriptionTierRaw = usage.SubscriptionTierRaw

	if len(usage.AntigravityQuota) > 0 {
		keys := make([]string, 0, len(usage.AntigravityQuota))
		for key := range usage.AntigravityQuota {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			q := usage.AntigravityQuota[key]
			if q == nil {
				continue
			}
			util := float64(q.Utilization)
			info.Dimensions = append(info.Dimensions, UpstreamQuotaDimension{
				Key:         "antigravity_model_" + key,
				Label:       key,
				Unit:        "percent",
				Utilization: &util,
				ResetsAt:    parseQuotaTime(q.ResetTime),
			})
		}
	}

	for _, credit := range usage.AICredits {
		amount := credit.Amount
		minimum := credit.MinimumBalance
		info.Credits = append(info.Credits, UpstreamQuotaCredit{
			Key:            "antigravity_credit_" + strings.ToLower(strings.TrimSpace(credit.CreditType)),
			Label:          credit.CreditType,
			Remaining:      &amount,
			MinimumBalance: &minimum,
		})
	}

	if usage.ErrorCode != "" || usage.Error != "" {
		info.State = "degraded"
		info.ErrorCode = usage.ErrorCode
		info.Error = usage.Error
	} else if len(info.Dimensions) > 0 || len(info.Credits) > 0 || info.SubscriptionTier != "" {
		info.State = "observed"
	}
	return info
}

func buildKiroUpstreamQuota(usage *UsageInfo) *UpstreamQuotaInfo {
	info := baseUpstreamQuota("kiro", usage, defaultUsageSource(usage))
	info.State = "unknown"
	if usage.KiroUsage == nil {
		return info
	}
	k := usage.KiroUsage
	current := k.Current
	limit := k.Limit
	percent := k.Percent
	remaining := limit - current
	if remaining < 0 {
		remaining = 0
	}
	info.Credits = append(info.Credits, UpstreamQuotaCredit{
		Key:         "kiro_credits",
		Label:       firstNonEmptyQuotaString(k.SubscriptionTitle, "Kiro credits"),
		Current:     &current,
		Limit:       &limit,
		Remaining:   &remaining,
		Utilization: &percent,
		ResetsAt:    parseQuotaTime(k.NextResetDate),
	})
	info.SubscriptionTierRaw = k.SubscriptionTitle
	if k.Trial != nil {
		trialCurrent := k.Trial.Current
		trialLimit := k.Trial.Limit
		trialPercent := k.Trial.Percent
		trialRemaining := trialLimit - trialCurrent
		if trialRemaining < 0 {
			trialRemaining = 0
		}
		info.Credits = append(info.Credits, UpstreamQuotaCredit{
			Key:         "kiro_trial",
			Label:       "Kiro trial",
			Current:     &trialCurrent,
			Limit:       &trialLimit,
			Remaining:   &trialRemaining,
			Utilization: &trialPercent,
			ExpiresAt:   k.Trial.ExpiresAt,
			Status:      k.Trial.Status,
		})
	}
	info.State = "observed"
	return info
}

func buildLocalAdapterUpstreamQuota(account *Account, usage *UsageInfo) *UpstreamQuotaInfo {
	if account.IsGrok() {
		return buildGrokUpstreamQuota(account, usage)
	}
	if account.Platform == PlatformNewAPI {
		return unsupportedUpstreamQuota("newapi", usage, "unsupported", "upstream quota is not configured for NewAPI accounts; TokenKey local usage windows are shown instead")
	}
	if account.Platform == PlatformAntigravity {
		return unsupportedUpstreamQuota("antigravity", usage, "relay_stub", "upstream quota is available on the edge OAuth account, not this relay stub")
	}
	return nil
}

func buildGrokUpstreamQuota(account *Account, usage *UsageInfo) *UpstreamQuotaInfo {
	info := baseUpstreamQuota("grok", usage, "headers")
	info.State = "unknown"
	snapshot, err := grokQuotaSnapshotFromExtra(account.Extra)
	if err != nil || snapshot == nil {
		info.ErrorCode = "quota_unknown"
		info.Error = "Grok quota is unknown until an xAI response includes rate-limit headers"
		return info
	}
	if t := parseQuotaTime(snapshot.UpdatedAt); t != nil {
		info.UpdatedAt = t
	}
	info.StatusCode = snapshot.StatusCode
	info.SubscriptionTier = snapshot.SubscriptionTier
	info.SubscriptionTierRaw = snapshot.SubscriptionTier
	info.EntitlementStatus = snapshot.EntitlementStatus
	info.RetryAfterSeconds = snapshot.RetryAfterSeconds
	addGrokQuotaWindow(info, "grok_requests", "Requests", "requests", snapshot.Requests)
	addGrokQuotaWindow(info, "grok_tokens", "Tokens", "tokens", snapshot.Tokens)
	if snapshot.HasObservedHeaders() {
		info.State = "observed"
	} else {
		info.State = "unknown"
		info.ErrorCode = "quota_unknown"
		info.Error = "No xAI quota headers observed on the latest Grok probe"
	}
	switch snapshot.StatusCode {
	case 401:
		info.State = "degraded"
		info.ErrorCode = "unauthenticated"
	case 403:
		info.State = "degraded"
		info.ErrorCode = "forbidden"
		if info.EntitlementStatus == "" {
			info.EntitlementStatus = "forbidden"
		}
	case 429:
		info.State = "degraded"
		info.ErrorCode = "rate_limited"
	}
	return info
}

func addGrokQuotaWindow(info *UpstreamQuotaInfo, key, label, unit string, window *xai.QuotaWindow) {
	if info == nil || window == nil {
		return
	}
	d := UpstreamQuotaDimension{
		Key:   key,
		Label: label,
		Unit:  unit,
	}
	if window.Limit != nil {
		v := float64(*window.Limit)
		d.Limit = &v
	}
	if window.Remaining != nil {
		v := float64(*window.Remaining)
		d.Remaining = &v
	}
	if d.Limit != nil && d.Remaining != nil {
		used := *d.Limit - *d.Remaining
		if used < 0 {
			used = 0
		}
		util := 0.0
		if *d.Limit > 0 {
			util = (used / *d.Limit) * 100
		}
		d.Used = &used
		d.Utilization = &util
	}
	if window.ResetAt != "" {
		d.ResetsAt = parseQuotaTime(window.ResetAt)
	} else if window.ResetUnix != nil {
		t := time.Unix(*window.ResetUnix, 0).UTC()
		d.ResetsAt = &t
	}
	info.Dimensions = append(info.Dimensions, d)
}

func progressToQuotaDimension(key, label, window string, progress *UsageProgress) *UpstreamQuotaDimension {
	if progress == nil {
		return nil
	}
	if progress.ResetsAt == nil && progress.LimitRequests <= 0 && progress.UsedRequests <= 0 && progress.Utilization <= 0 {
		return nil
	}
	d := &UpstreamQuotaDimension{
		Key:         key,
		Label:       label,
		Unit:        "percent",
		Window:      window,
		Utilization: &progress.Utilization,
		ResetsAt:    progress.ResetsAt,
	}
	if progress.LimitRequests > 0 {
		used := float64(progress.UsedRequests)
		limit := float64(progress.LimitRequests)
		remaining := limit - used
		if remaining < 0 {
			remaining = 0
		}
		d.Unit = "requests"
		d.Used = &used
		d.Limit = &limit
		d.Remaining = &remaining
	}
	return d
}

func baseUpstreamQuota(provider string, usage *UsageInfo, source string) *UpstreamQuotaInfo {
	return &UpstreamQuotaInfo{
		Provider:  provider,
		Source:    source,
		UpdatedAt: usage.UpdatedAt,
	}
}

func unsupportedUpstreamQuota(provider string, usage *UsageInfo, source, msg string) *UpstreamQuotaInfo {
	info := baseUpstreamQuota(provider, usage, source)
	info.State = "unsupported"
	info.ErrorCode = "unsupported"
	info.Error = msg
	return info
}

func defaultUsageSource(usage *UsageInfo) string {
	if usage != nil && strings.TrimSpace(usage.Source) != "" {
		return usage.Source
	}
	return "active"
}

func parseQuotaTime(raw string) *time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return &t
		}
	}
	return nil
}

func firstNonEmptyQuotaString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
