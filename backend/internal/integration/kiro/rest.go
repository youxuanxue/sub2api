package kiro

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

const (
	kiroRestAPIBase = "https://codewhisperer.us-east-1.amazonaws.com"
	// kiroManagementAPIBase is the go-forward *.kiro.dev control-plane host
	// (configuration / lifecycle / access management). It replaces the legacy
	// codewhisperer.* base, which Kiro's docs no longer list at all. Used (via
	// kiroRestFetch, management-first with codewhisperer fallback) by the calls
	// edge-us6 smoke-validated equivalent on management: ListAvailableProfiles
	// (same profileArn), ListAvailableModels (same model set), getUsageLimits (all
	// fields UsageLimitsResponse reads). GetUserInfo stays on codewhisperer: the new
	// protocol has no standalone user-info op — identity is folded into getUsageLimits
	// (userInfo{email,userId}); and kiro.GetUserInfo has no TK caller anyway.
	kiroManagementAPIBase = "https://management.us-east-1.kiro.dev"
)

// kiroRestBases lists the control-plane hosts in preference order: the go-forward
// management.us-east-1.kiro.dev first, the legacy codewhisperer.* as fallback.
func kiroRestBases() []string { return []string{kiroManagementAPIBase, kiroRestAPIBase} }

// kiroRestFetch issues method+path against each control-plane base in turn
// (profileArn appended when withParn), returning the first HTTP-200 body. Used by
// the calls edge-us6 smoke-validated equivalent on management — ListAvailableProfiles
// (same profileArn), ListAvailableModels (same model set), getUsageLimits (every
// field UsageLimitsResponse reads is present). GetUserInfo is NOT routed here: the
// new *.kiro.dev protocol has no standalone user-info operation (management 400s
// every GetUserInfo/getUserInfo/GetUser variant, edge-us6-probed); user identity is
// instead folded into getUsageLimits (userInfo{email,userId} + subscriptionInfo),
// which TK already parses. kiro.GetUserInfo has no caller in TK anyway.
func kiroRestFetch(account *Account, method, path, body string, withParn bool) ([]byte, error) {
	if withParn {
		if err := ensureProfileArn(account); err != nil {
			return nil, err
		}
	}
	data, err := kiroRestFetchBases(account, method, path, body, withParn)
	if withParn && isInvalidProfileArnError(err) {
		logWarnf("[KiroREST] stale profileArn for %s, re-resolving: %v", accountEmail(account), err)
		if resolveErr := reresolveProfileArnAfterStale(account); resolveErr == nil {
			return kiroRestFetchBases(account, method, path, body, withParn)
		}
	}
	return data, err
}

func kiroRestFetchBases(account *Account, method, path, body string, withParn bool) ([]byte, error) {
	var lastErr error
	for _, base := range kiroRestBases() {
		u := base + path
		if withParn {
			u = withProfileArnQuery(u, account)
		}
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, err := http.NewRequest(method, u, rdr)
		if err != nil {
			lastErr = err
			continue
		}
		setKiroHeaders(req, account)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := GetRestClientForProxy(ResolveAccountProxyURL(account)).Do(req)
		if err != nil {
			lastErr = err
			logWarnf("[KiroREST] %s %s failed: %v", method, base, err)
			continue
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			lastErr = fmt.Errorf("HTTP %d from %s: %s", resp.StatusCode, base, string(data))
			logWarnf("[KiroREST] %s %s -> HTTP %d", method, base, resp.StatusCode)
			continue
		}
		return data, nil
	}
	return nil, lastErr
}

func ensureProfileArn(account *Account) error {
	if account == nil {
		return fmt.Errorf("account is nil")
	}
	_, err := ResolveProfileArn(account)
	return err
}

// reresolveProfileArnAfterStale clears a cached profileArn and fetches a fresh one.
// Used when upstream returns HTTP 400 Invalid profileArn for REST and chat paths.
func reresolveProfileArnAfterStale(account *Account) error {
	if account == nil {
		return fmt.Errorf("account is nil")
	}
	account.ProfileArn = ""
	return ensureProfileArn(account)
}

func isInvalidProfileArnError(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "invalid profilearn")
}

func accountEmail(account *Account) string {
	if account == nil {
		return ""
	}
	return account.Email
}

// GetUsageLimits 获取账户使用量和订阅信息
func GetUsageLimits(account *Account) (*UsageLimitsResponse, error) {
	data, err := kiroRestFetch(account, "GET", "/getUsageLimits?origin=AI_EDITOR&resourceType=AGENTIC_REQUEST&isEmailRequired=true", "", true)
	if err != nil {
		return nil, err
	}
	var result UsageLimitsResponse
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

// GetUserInfo 获取用户信息. Legacy codewhisperer.* only — the new *.kiro.dev protocol
// has NO standalone user-info operation (management 400s every variant; edge-us6 probe
// 2026-06-26). On the new protocol user identity is returned inside getUsageLimits
// (userInfo{email,userId} + subscriptionInfo), already parsed via the migrated
// GetUsageLimits. This function additionally has no caller in TK, so it needs no
// migration; kept as vendored API surface.
func GetUserInfo(account *Account) (*UserInfoResponse, error) {
	url := fmt.Sprintf("%s/GetUserInfo", kiroRestAPIBase)

	payload := `{"origin":"KIRO_IDE"}`
	req, err := http.NewRequest("POST", url, strings.NewReader(payload))
	if err != nil {
		return nil, err
	}

	setKiroHeaders(req, account)
	req.Header.Set("Content-Type", "application/json")

	resp, err := GetRestClientForProxy(ResolveAccountProxyURL(account)).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var result UserInfoResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, err
	}
	return &result, nil
}

// ListAvailableModels 获取可用模型列表
func ListAvailableModels(account *Account) ([]ModelInfo, error) {
	data, err := kiroRestFetch(account, "GET", "/ListAvailableModels?origin=AI_EDITOR&maxResults=50", "", true)
	if err != nil {
		return nil, err
	}
	var result struct {
		Models []ModelInfo `json:"models"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return result.Models, nil
}

// ResolveProfileArn returns the account profile ARN, fetching and caching it
// when it is missing. First tries ListAvailableProfiles; if that returns empty,
// falls back to refreshing the token (which returns profileArn in the response).
func ResolveProfileArn(account *Account) (string, error) {
	if account == nil {
		return "", fmt.Errorf("account is nil")
	}
	if profileArn := strings.TrimSpace(account.ProfileArn); profileArn != "" {
		return profileArn, nil
	}

	// Try ListAvailableProfiles first, retrying on transient failures.
	// NOTE: persistence removed (vendor package does not write a DB). The
	// resolved ARN is cached only on the in-memory account; the TokenKey layer
	// is responsible for persisting account.ProfileArn after this returns.
	profileArn, err := listAvailableProfilesWithRetry(account)
	if err == nil && profileArn != "" {
		account.ProfileArn = profileArn
		return profileArn, nil
	}

	// Fallback: refresh token to get profileArn from auth response
	if account.RefreshToken != "" {
		_, _, _, refreshedArn, refreshErr := RefreshToken(account)
		if refreshErr == nil && refreshedArn != "" {
			account.ProfileArn = refreshedArn
			return refreshedArn, nil
		}
	}

	return "", fmt.Errorf("no available Kiro profile")
}

func listAvailableProfilesWithRetry(account *Account) (string, error) {
	// Retry transient failures (network errors, 5xx, 429) with short backoff.
	// An empty profile list or 4xx (other than 429) is treated as authoritative
	// and not retried — they reflect account state, not upstream flakiness.
	const maxAttempts = 3
	backoff := 200 * time.Millisecond

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		profileArn, err := listAvailableProfiles(account)
		if err == nil {
			return profileArn, nil
		}
		lastErr = err
		if !isTransientProfileFetchError(err) || attempt == maxAttempts {
			return "", err
		}
		logDebugf("[ProfileArn] ListAvailableProfiles transient failure for %s (attempt %d/%d): %v",
			account.Email, attempt, maxAttempts, err)
		time.Sleep(backoff)
		backoff *= 2
	}
	return "", lastErr
}

// isTransientProfileFetchError reports whether a ListAvailableProfiles error
// is worth retrying. Network errors and upstream 5xx/429 are transient; other
// HTTP errors and an empty profile list are not.
func isTransientProfileFetchError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	if strings.Contains(msg, "empty profile list") {
		return false
	}
	if strings.HasPrefix(msg, "HTTP ") {
		return strings.HasPrefix(msg, "HTTP 5") || strings.HasPrefix(msg, "HTTP 429")
	}
	// Non-HTTP errors are network/transport level — retry.
	return true
}

func listAvailableProfiles(account *Account) (string, error) {
	data, err := kiroRestFetch(account, "POST", "/ListAvailableProfiles", `{"maxResults":10}`, false)
	if err != nil {
		return "", err
	}
	var result struct {
		Profiles []struct {
			Arn string `json:"arn"`
		} `json:"profiles"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	for _, profile := range result.Profiles {
		if profileArn := strings.TrimSpace(profile.Arn); profileArn != "" {
			return profileArn, nil
		}
	}
	return "", fmt.Errorf("empty profile list")
}

func withProfileArnQuery(rawURL string, account *Account) string {
	if account == nil {
		return rawURL
	}
	profileArn := strings.TrimSpace(account.ProfileArn)
	if profileArn == "" {
		return rawURL
	}
	return rawURL + "&profileArn=" + neturl.QueryEscape(profileArn)
}

func setKiroHeaders(req *http.Request, account *Account) {
	host := ""
	if req.URL != nil {
		host = req.URL.Host
	}
	headerValues := buildRuntimeHeaderValues(account, host)

	req.Header.Set("Accept", "application/json")
	applyKiroBaseHeaders(req, account, headerValues)
}

// RefreshAccountInfo 刷新账户信息（使用量、订阅等）
func RefreshAccountInfo(account *Account) (*AccountInfo, error) {
	info := &AccountInfo{
		LastRefresh: time.Now().Unix(),
	}

	// 获取使用量和订阅信息
	//
	// NOTE: DB side effects removed (vendor package does not persist account
	// state). Ban / suspended / auth detection now surfaces purely through the
	// returned error — the TokenKey layer inspects the error and decides whether
	// to disable/ban the ent account. We deliberately do NOT mutate the passed
	// *Account here so callers retain a single source of truth.
	usage, err := GetUsageLimits(account)
	if err != nil {
		// 检测封禁状态
		errMsg := err.Error()
		if strings.Contains(errMsg, "TEMPORARILY_SUSPENDED") {
			logWarnf("[RefreshAccountInfo] Account %s is temporarily suspended: %v", account.Email, err)
			return nil, fmt.Errorf("account suspended: %w", err)
		} else if strings.Contains(errMsg, "403") || strings.Contains(errMsg, "401") ||
			strings.Contains(errMsg, "invalid") || strings.Contains(errMsg, "expired") {
			// Token 相关错误，可能需要重新认证
			logWarnf("[RefreshAccountInfo] Authentication error for %s: %v", account.Email, err)
		}

		return nil, fmt.Errorf("GetUsageLimits: %w", err)
	}

	// 成功获取信息：若账户先前被标记封禁，记录其已恢复（不再写库）。
	if account.BanStatus != "" && account.BanStatus != "ACTIVE" {
		logInfof("[RefreshAccountInfo] Account %s is now active (ban status cleared by caller)", account.Email)
	}

	// 解析用户信息
	if usage.UserInfo != nil {
		info.Email = usage.UserInfo.Email
		info.UserId = usage.UserInfo.UserId
	}

	// 解析订阅信息
	if usage.SubscriptionInfo != nil {
		// 优先从 SubscriptionTitle 或 SubscriptionName 解析类型
		titleOrName := usage.SubscriptionInfo.SubscriptionTitle
		if titleOrName == "" {
			titleOrName = usage.SubscriptionInfo.SubscriptionName
		}
		if titleOrName == "" {
			titleOrName = usage.SubscriptionInfo.SubscriptionType
		}
		info.SubscriptionType = parseSubscriptionType(titleOrName)
		info.SubscriptionTitle = usage.SubscriptionInfo.SubscriptionTitle
		if info.SubscriptionTitle == "" {
			info.SubscriptionTitle = usage.SubscriptionInfo.SubscriptionName
		}
		logDebugf("[RefreshAccountInfo] Subscription: type=%s, title=%s, name=%s, parsed=%s",
			usage.SubscriptionInfo.SubscriptionType,
			usage.SubscriptionInfo.SubscriptionTitle,
			usage.SubscriptionInfo.SubscriptionName,
			info.SubscriptionType)
	}

	// 解析使用量
	if len(usage.UsageBreakdownList) > 0 {
		breakdown := usage.UsageBreakdownList[0]
		info.UsageCurrent = breakdown.CurrentUsage
		info.UsageLimit = breakdown.UsageLimit
		if info.UsageLimit > 0 {
			info.UsagePercent = info.UsageCurrent / info.UsageLimit
		}
	}

	// 解析重置日期
	if usage.NextDateReset != "" {
		if ts, err := usage.NextDateReset.Int64(); err == nil && ts > 0 {
			info.NextResetDate = time.Unix(ts, 0).Format("2006-01-02")
		} else if f, err := usage.NextDateReset.Float64(); err == nil && f > 0 {
			info.NextResetDate = time.Unix(int64(f), 0).Format("2006-01-02")
		}
	}

	// 解析试用配额信息
	if len(usage.UsageBreakdownList) > 0 {
		breakdown := usage.UsageBreakdownList[0]
		if breakdown.FreeTrialInfo != nil {
			info.TrialUsageCurrent = breakdown.FreeTrialInfo.CurrentUsage
			info.TrialUsageLimit = breakdown.FreeTrialInfo.UsageLimit
			if info.TrialUsageLimit > 0 {
				info.TrialUsagePercent = info.TrialUsageCurrent / info.TrialUsageLimit
			}
			info.TrialStatus = breakdown.FreeTrialInfo.FreeTrialStatus

			// 解析试用到期时间
			if breakdown.FreeTrialInfo.FreeTrialExpiry != "" {
				if ts, err := breakdown.FreeTrialInfo.FreeTrialExpiry.Int64(); err == nil && ts > 0 {
					info.TrialExpiresAt = ts
				} else if f, err := breakdown.FreeTrialInfo.FreeTrialExpiry.Float64(); err == nil && f > 0 {
					info.TrialExpiresAt = int64(f)
				}
			}
		}
	}

	return info, nil
}

func parseSubscriptionType(raw string) string {
	upper := strings.ToUpper(raw)
	if strings.Contains(upper, "PRO_PLUS") || strings.Contains(upper, "PROPLUS") {
		return "PRO_PLUS"
	}
	if strings.Contains(upper, "POWER") {
		return "POWER"
	}
	if strings.Contains(upper, "PRO") {
		return "PRO"
	}
	return "FREE"
}

// 响应结构体
type UsageLimitsResponse struct {
	UsageBreakdownList []UsageBreakdown  `json:"usageBreakdownList"`
	NextDateReset      json.Number       `json:"nextDateReset"`
	SubscriptionInfo   *SubscriptionInfo `json:"subscriptionInfo"`
	UserInfo           *UserInfo         `json:"userInfo"`
}

type UsageBreakdown struct {
	ResourceType  string         `json:"resourceType"`
	CurrentUsage  float64        `json:"currentUsage"`
	UsageLimit    float64        `json:"usageLimit"`
	Currency      string         `json:"currency"`
	Unit          string         `json:"unit"`
	OverageRate   float64        `json:"overageRate"`
	FreeTrialInfo *FreeTrialInfo `json:"freeTrialInfo"`
	Bonuses       []BonusInfo    `json:"bonuses"`
}

type FreeTrialInfo struct {
	CurrentUsage    float64     `json:"currentUsage"`
	UsageLimit      float64     `json:"usageLimit"`
	FreeTrialStatus string      `json:"freeTrialStatus"`
	FreeTrialExpiry json.Number `json:"freeTrialExpiry"`
}

type BonusInfo struct {
	BonusCode    string      `json:"bonusCode"`
	DisplayName  string      `json:"displayName"`
	CurrentUsage float64     `json:"currentUsage"`
	UsageLimit   float64     `json:"usageLimit"`
	ExpiresAt    json.Number `json:"expiresAt"`
	Status       string      `json:"status"`
}

type SubscriptionInfo struct {
	SubscriptionName  string `json:"subscriptionName"`
	SubscriptionTitle string `json:"subscriptionTitle"`
	SubscriptionType  string `json:"subscriptionType"`
	Status            string `json:"status"`
	UpgradeCapability string `json:"upgradeCapability"`
}

type UserInfo struct {
	Email  string `json:"email"`
	UserId string `json:"userId"`
}

type UserInfoResponse struct {
	Email  string `json:"email"`
	UserId string `json:"userId"`
	Idp    string `json:"idp"`
	Status string `json:"status"`
}

type ModelInfo struct {
	ModelId        string   `json:"modelId"`
	ModelName      string   `json:"modelName"`
	Description    string   `json:"description"`
	InputTypes     []string `json:"supportedInputTypes"`
	RateMultiplier float64  `json:"rateMultiplier"`
	TokenLimits    *struct {
		MaxInputTokens  int `json:"maxInputTokens"`
		MaxOutputTokens int `json:"maxOutputTokens"`
	} `json:"tokenLimits"`
}
