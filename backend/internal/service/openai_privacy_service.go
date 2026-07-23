package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/imroc/req/v3"
)

// PrivacyClientFactory creates an HTTP client for privacy API calls.
// Injected from repository layer to avoid import cycles.
type PrivacyClientFactory func(proxyURL string) (*req.Client, error)

// openAISettingsURL is the training-toggle PATCH endpoint. var (not const) only so
// tests can point it at an httptest server, mirroring openAISettingsUserURL (the read side).
var openAISettingsURL = "https://chatgpt.com/backend-api/settings/account_user_setting"

const (
	PrivacyModeTrainingOff = "training_off"
	PrivacyModeFailed      = "training_set_failed"
	PrivacyModeCFBlocked   = "training_set_cf_blocked"
)

func shouldSkipOpenAIPrivacyEnsure(extra map[string]any) bool {
	if extra == nil {
		return false
	}
	raw, ok := extra["privacy_mode"]
	if !ok {
		return false
	}
	mode, _ := raw.(string)
	mode = strings.TrimSpace(mode)
	return mode != PrivacyModeFailed && mode != PrivacyModeCFBlocked
}

// disableOpenAITraining calls ChatGPT settings API to turn off "Improve the model for everyone".
// Returns privacy_mode value: "training_off" on success, "cf_blocked" / "failed" on failure.
func disableOpenAITraining(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL string) string {
	if accessToken == "" || clientFactory == nil {
		return ""
	}

	// TK: read-first. The settings PATCH below is Cloudflare-challenged from a datacenter
	// egress (-> training_set_cf_blocked) even when training is already disabled. A GET of
	// the same settings resource is not challenged, so if upstream training_allowed is
	// already false we record training_off without issuing the (blocked) PATCH. An
	// inconclusive read (error / non-2xx / unparseable) falls through to the PATCH path
	// unchanged. Read+parse live in openai_privacy_tk_read.go to keep this a thin call site.
	if disabled, ok := readOpenAITrainingDisabled(ctx, clientFactory, accessToken, proxyURL); ok && disabled {
		slog.Info("openai_privacy_read_already_disabled")
		return PrivacyModeTrainingOff
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Warn("openai_privacy_client_error", "error", err.Error())
		return PrivacyModeFailed
	}

	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetHeader("sec-fetch-mode", "cors").
		SetHeader("sec-fetch-site", "same-origin").
		SetHeader("sec-fetch-dest", "empty").
		SetQueryParam("feature", "training_allowed").
		SetQueryParam("value", "false").
		Patch(openAISettingsURL)

	if err != nil {
		slog.Warn("openai_privacy_request_error", "error", err.Error())
		return PrivacyModeFailed
	}

	// TK: classification (incl. broadened anti-bot/CF-challenge detection) lives in
	// openai_privacy_tk_classify.go to keep this upstream file a thin call site.
	switch mode := classifyOpenAIPrivacyResponse(resp.StatusCode, resp.GetContentType(), resp.String()); mode {
	case PrivacyModeCFBlocked:
		slog.Warn("openai_privacy_cf_blocked", "status", resp.StatusCode, "content_type", resp.GetContentType())
		return mode
	case PrivacyModeTrainingOff:
		slog.Info("openai_privacy_training_disabled")
		return mode
	default:
		// truncate at 2000B (was 200B): OpenAI privacy API failure responses can include
		// nested HTML/JSON error envelopes, request-id, and rate-limit hints; 200B routinely
		// cut these off mid-key and forced operators to re-enable debug logging to root-cause
		// (see prod incident on 2026-04: "Privacy not set" loop on GPT-A1).
		slog.Warn("openai_privacy_failed", "status", resp.StatusCode, "content_type", resp.GetContentType(), "body", truncate(resp.String(), 2000))
		return mode
	}
}

// ChatGPTAccountInfo 从 chatgpt.com/backend-api/accounts/check 获取的账号信息
type ChatGPTAccountInfo struct {
	PlanType              string
	Email                 string
	SubscriptionExpiresAt string // entitlement.expires_at (RFC3339)
}

var (
	chatGPTAccountsCheckURL = "https://chatgpt.com/backend-api/accounts/check/v4-2023-04-27"
	chatGPTSubscriptionsURL = "https://chatgpt.com/backend-api/subscriptions"
)

// fetchChatGPTAccountsCheck loads the accounts/check payload (best-effort).
func fetchChatGPTAccountsCheck(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL string) map[string]any {
	if accessToken == "" || clientFactory == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Debug("chatgpt_account_check_client_error", "error", err.Error())
		return nil
	}

	var result map[string]any
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetSuccessResult(&result).
		Get(chatGPTAccountsCheckURL)

	if err != nil {
		slog.Debug("chatgpt_account_check_request_error", "error", err.Error())
		return nil
	}

	if !resp.IsSuccessState() {
		slog.Debug("chatgpt_account_check_failed", "status", resp.StatusCode, "body", truncate(resp.String(), 200))
		return nil
	}

	accounts, ok := result["accounts"].(map[string]any)
	if !ok {
		slog.Debug("chatgpt_account_check_no_accounts", "body", truncate(resp.String(), 300))
		return nil
	}
	return accounts
}

func parseChatGPTAccountInfo(accounts map[string]any, orgID string) *ChatGPTAccountInfo {
	if accounts == nil {
		return nil
	}
	info := &ChatGPTAccountInfo{}
	now := time.Now()

	// 优先匹配 orgID 对应的账号（access_token JWT 中的 poid）
	if orgID != "" {
		if acctRaw, ok := accounts[orgID]; ok {
			if acct, ok := acctRaw.(map[string]any); ok {
				if isUsableChatGPTAccountCandidate(acct, now) {
					fillAccountInfo(info, acct)
				}
			}
		}
	}

	// 未匹配到时，遍历所有账号：优先 is_default，次选非 free
	if info.PlanType == "" {
		type candidate struct {
			planType  string
			expiresAt string
		}
		var defaultC, paidC, anyC candidate
		for _, acctRaw := range accounts {
			acct, ok := acctRaw.(map[string]any)
			if !ok {
				continue
			}
			if !isUsableChatGPTAccountCandidate(acct, now) {
				continue
			}
			planType := extractPlanType(acct)
			if planType == "" {
				continue
			}
			ea := extractEntitlementExpiresAt(acct)
			if anyC.planType == "" {
				anyC = candidate{planType, ea}
			}
			if account, ok := acct["account"].(map[string]any); ok {
				if isDefault, _ := account["is_default"].(bool); isDefault {
					defaultC = candidate{planType, ea}
				}
			}
			if !strings.EqualFold(planType, "free") && paidC.planType == "" {
				paidC = candidate{planType, ea}
			}
		}
		// 优先级：default > 非 free > 任意
		switch {
		case defaultC.planType != "":
			info.PlanType, info.SubscriptionExpiresAt = defaultC.planType, defaultC.expiresAt
		case paidC.planType != "":
			info.PlanType, info.SubscriptionExpiresAt = paidC.planType, paidC.expiresAt
		default:
			info.PlanType, info.SubscriptionExpiresAt = anyC.planType, anyC.expiresAt
		}
	}

	// orgID 命中时 plan_type 可能已有值但 entitlement.expires_at 为空（Plus/Pro 常见）。
	// 此时上面的遍历会被跳过，仍需扫描其它 workspace 的 entitlement 或留给 subscriptions 回退。
	if info.SubscriptionExpiresAt == "" {
		info.SubscriptionExpiresAt = findBestSubscriptionExpiresAtFromAccounts(accounts, now)
	}

	if info.PlanType == "" {
		slog.Debug("chatgpt_account_check_no_plan_type", "org_id", orgID)
		return nil
	}

	slog.Info("chatgpt_account_check_success", "plan_type", info.PlanType, "subscription_expires_at", info.SubscriptionExpiresAt, "org_id", orgID)
	return info
}

func findBestSubscriptionExpiresAtFromAccounts(accounts map[string]any, now time.Time) string {
	type candidate struct {
		expiresAt string
		paid      bool
		defaulted bool
	}
	var best candidate
	for _, acctRaw := range accounts {
		acct, ok := acctRaw.(map[string]any)
		if !ok || !isUsableChatGPTAccountCandidate(acct, now) {
			continue
		}
		expiresAt := strings.TrimSpace(extractEntitlementExpiresAt(acct))
		if expiresAt == "" {
			continue
		}
		if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
			continue
		}
		planType := extractPlanType(acct)
		isPaid := planType != "" && !strings.EqualFold(planType, "free") && !strings.EqualFold(planType, "basic")
		isDefault := false
		if account, ok := acct["account"].(map[string]any); ok {
			isDefault, _ = account["is_default"].(bool)
		}
		cur := candidate{expiresAt: expiresAt, paid: isPaid, defaulted: isDefault}
		if best.expiresAt == "" ||
			(cur.defaulted && !best.defaulted) ||
			(cur.defaulted == best.defaulted && cur.paid && !best.paid) {
			best = cur
		}
	}
	return best.expiresAt
}

func collectChatGPTSubscriptionAccountIDCandidates(
	chatGPTAccountID, organizationID, orgID string,
	accounts map[string]any,
) []string {
	seen := make(map[string]struct{})
	add := func(ids []string, id string) []string {
		id = strings.TrimSpace(id)
		if id == "" {
			return ids
		}
		if _, ok := seen[id]; ok {
			return ids
		}
		seen[id] = struct{}{}
		return append(ids, id)
	}

	candidates := make([]string, 0, 4)
	for _, id := range []string{chatGPTAccountID, organizationID, orgID} {
		candidates = add(candidates, id)
	}

	if accounts != nil {
		type keyed struct {
			id        string
			paid      bool
			defaulted bool
		}
		var keyedAccounts []keyed
		for accountID, acctRaw := range accounts {
			acct, ok := acctRaw.(map[string]any)
			if !ok || !isUsableChatGPTAccountCandidate(acct, time.Now()) {
				continue
			}
			planType := extractPlanType(acct)
			isPaid := planType != "" && !strings.EqualFold(planType, "free") && !strings.EqualFold(planType, "basic")
			isDefault := false
			if account, ok := acct["account"].(map[string]any); ok {
				isDefault, _ = account["is_default"].(bool)
			}
			keyedAccounts = append(keyedAccounts, keyed{id: accountID, paid: isPaid, defaulted: isDefault})
		}
		for _, preferPaid := range []bool{true, false} {
			for _, preferDefault := range []bool{true, false} {
				for _, item := range keyedAccounts {
					if preferPaid && !item.paid {
						continue
					}
					if preferDefault && !item.defaulted {
						continue
					}
					candidates = add(candidates, item.id)
				}
			}
		}
	}
	return candidates
}

func fetchChatGPTSubscriptionExpiresAtWithCandidates(
	ctx context.Context,
	clientFactory PrivacyClientFactory,
	accessToken, proxyURL string,
	accountIDs []string,
) string {
	for _, accountID := range accountIDs {
		if expiresAt := fetchChatGPTSubscriptionExpiresAt(ctx, clientFactory, accessToken, proxyURL, accountID); expiresAt != "" {
			return expiresAt
		}
	}
	return ""
}

// fetchOpenAISubscriptionExpiresAt resolves subscription expiry for a stored OpenAI OAuth account.
func fetchOpenAISubscriptionExpiresAt(
	ctx context.Context,
	clientFactory PrivacyClientFactory,
	accessToken, proxyURL, chatGPTAccountID, organizationID, orgID string,
) string {
	if accessToken == "" || clientFactory == nil {
		return ""
	}

	accounts := fetchChatGPTAccountsCheck(ctx, clientFactory, accessToken, proxyURL)
	if info := parseChatGPTAccountInfo(accounts, orgID); info != nil && info.SubscriptionExpiresAt != "" {
		return info.SubscriptionExpiresAt
	}

	candidates := collectChatGPTSubscriptionAccountIDCandidates(chatGPTAccountID, organizationID, orgID, accounts)
	return fetchChatGPTSubscriptionExpiresAtWithCandidates(ctx, clientFactory, accessToken, proxyURL, candidates)
}

// fetchChatGPTSubscriptionExpiresAt reads the lightweight subscription endpoint used by
// ChatGPT/Codex clients. Some Plus accounts no longer expose entitlement.expires_at in
// accounts/check, but this endpoint still returns active_until.
func fetchChatGPTSubscriptionExpiresAt(ctx context.Context, clientFactory PrivacyClientFactory, accessToken, proxyURL, accountID string) string {
	accountID = strings.TrimSpace(accountID)
	if accessToken == "" || accountID == "" || clientFactory == nil {
		return ""
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	client, err := clientFactory(proxyURL)
	if err != nil {
		slog.Debug("chatgpt_subscription_client_error", "error", err.Error())
		return ""
	}

	var result struct {
		PlanType    string `json:"plan_type"`
		ActiveUntil string `json:"active_until"`
		WillRenew   bool   `json:"will_renew"`
		ID          string `json:"id"`
	}
	resp, err := client.R().
		SetContext(ctx).
		SetHeader("Authorization", "Bearer "+accessToken).
		SetHeader("Origin", "https://chatgpt.com").
		SetHeader("Referer", "https://chatgpt.com/").
		SetHeader("Accept", "application/json").
		SetSuccessResult(&result).
		SetQueryParam("account_id", accountID).
		Get(chatGPTSubscriptionsURL)
	if err != nil {
		slog.Debug("chatgpt_subscription_request_error", "error", err.Error())
		return ""
	}
	if !resp.IsSuccessState() {
		slog.Debug("chatgpt_subscription_failed", "status", resp.StatusCode, "body", truncate(resp.String(), 200))
		return ""
	}

	activeUntil := strings.TrimSpace(result.ActiveUntil)
	if activeUntil == "" {
		slog.Debug("chatgpt_subscription_no_active_until", "plan_type", result.PlanType, "has_subscription_id", strings.TrimSpace(result.ID) != "", "will_renew", result.WillRenew)
		return ""
	}
	if _, err := time.Parse(time.RFC3339, activeUntil); err != nil {
		slog.Debug("chatgpt_subscription_bad_active_until", "active_until", activeUntil, "error", err.Error())
		return ""
	}

	slog.Info("chatgpt_subscription_success", "plan_type", result.PlanType, "subscription_expires_at", activeUntil, "account_id", accountID)
	return activeUntil
}

// fillAccountInfo 从单个 account 对象中提取 plan_type 和 subscription_expires_at
func fillAccountInfo(info *ChatGPTAccountInfo, acct map[string]any) {
	info.PlanType = extractPlanType(acct)
	info.SubscriptionExpiresAt = extractEntitlementExpiresAt(acct)
}

// extractPlanType 从单个 account 对象中提取 plan_type
func extractPlanType(acct map[string]any) string {
	if account, ok := acct["account"].(map[string]any); ok {
		if planType, ok := account["plan_type"].(string); ok && planType != "" {
			return planType
		}
	}
	if entitlement, ok := acct["entitlement"].(map[string]any); ok {
		if subPlan, ok := entitlement["subscription_plan"].(string); ok && subPlan != "" {
			return subPlan
		}
	}
	return ""
}

func isUsableChatGPTAccountCandidate(acct map[string]any, now time.Time) bool {
	if acct == nil || hasChatGPTAccountDeactivatedMarker(acct) {
		return false
	}
	if account, ok := acct["account"].(map[string]any); ok && hasChatGPTAccountDeactivatedMarker(account) {
		return false
	}

	expiresAt := extractEntitlementExpiresAt(acct)
	if expiresAt == "" {
		return true
	}
	expiry, err := time.Parse(time.RFC3339, expiresAt)
	if err != nil {
		return true
	}
	return expiry.After(now)
}

func hasChatGPTAccountDeactivatedMarker(obj map[string]any) bool {
	for _, key := range []string{"deactivated", "is_deactivated", "disabled", "is_disabled"} {
		if value, ok := obj[key].(bool); ok && value {
			return true
		}
	}
	for _, key := range []string{"deactivated_at", "disabled_at", "deleted_at"} {
		if value, ok := obj[key].(string); ok && strings.TrimSpace(value) != "" {
			return true
		}
	}
	for _, key := range []string{"status", "state"} {
		value, _ := obj[key].(string)
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "deactivated", "disabled", "deleted", "inactive", "suspended":
			return true
		}
	}
	return false
}

// extractEntitlementExpiresAt 从 entitlement 中提取 expires_at。
// 预期为 RFC3339 字符串格式，如 "2026-05-02T20:32:12+00:00"。
func extractEntitlementExpiresAt(acct map[string]any) string {
	entitlement, ok := acct["entitlement"].(map[string]any)
	if !ok {
		return ""
	}
	ea, _ := entitlement["expires_at"].(string)
	return ea
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("...(%d more)", len(s)-n)
}
