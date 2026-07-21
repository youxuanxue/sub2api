package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

const antigravityImportClockSkewSeconds int64 = 120

type AntigravityOAuthImportRequest struct {
	Content                 string         `json:"content"`
	Contents                []string       `json:"contents"`
	Name                    string         `json:"name"`
	Notes                   *string        `json:"notes"`
	GroupIDs                []int64        `json:"group_ids"`
	ProxyID                 *int64         `json:"proxy_id"`
	Concurrency             *int           `json:"concurrency"`
	Priority                *int           `json:"priority"`
	RateMultiplier          *float64       `json:"rate_multiplier"`
	LoadFactor              *int           `json:"load_factor"`
	ExpiresAt               *int64         `json:"expires_at"`
	AutoPauseOnExpired      *bool          `json:"auto_pause_on_expired"`
	Extra                   map[string]any `json:"extra"`
	UpdateExisting          *bool          `json:"update_existing"`
	SkipDefaultGroupBind    *bool          `json:"skip_default_group_bind"`
	ConfirmMixedChannelRisk *bool          `json:"confirm_mixed_channel_risk"`
	FillProjectID           *bool          `json:"fill_project_id"`
}

type AntigravityOAuthImportResult struct {
	Total    int                             `json:"total"`
	Created  int                             `json:"created"`
	Updated  int                             `json:"updated"`
	Skipped  int                             `json:"skipped"`
	Failed   int                             `json:"failed"`
	Items    []AntigravityOAuthImportItem    `json:"items,omitempty"`
	Warnings []AntigravityOAuthImportMessage `json:"warnings,omitempty"`
	Errors   []AntigravityOAuthImportMessage `json:"errors,omitempty"`
}

type AntigravityOAuthImportItem struct {
	Index     int    `json:"index"`
	Name      string `json:"name,omitempty"`
	Action    string `json:"action"`
	AccountID int64  `json:"account_id,omitempty"`
	Message   string `json:"message,omitempty"`
}

type AntigravityOAuthImportMessage struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	Message string `json:"message"`
}

type antigravityImportEntry struct {
	Index int
	Value any
}

type antigravityImportAccount struct {
	Name          string
	Email         string
	ProjectID     string
	AccessToken   string
	RefreshToken  string
	TokenType     string
	ExpiresAtUnix int64
	Credentials   map[string]any
	Extra         map[string]any
	IdentityKeys  []string
	WarningTexts  []string
}

type antigravityAccountIndex struct {
	accountsByKey map[string][]service.Account
}

// ImportAntigravityOAuth imports Antigravity OAuth credentials from JSON exports
// (for example antigravity_*.json files produced by local OAuth tooling).
// POST /api/v1/admin/accounts/import/antigravity-oauth
func (h *AccountHandler) ImportAntigravityOAuth(c *gin.Context) {
	var req AntigravityOAuthImportRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.InvalidRequest(c)
		return
	}
	if req.Concurrency != nil && *req.Concurrency < 0 {
		response.BadRequest(c, "concurrency must be >= 0")
		return
	}
	if req.Priority != nil && *req.Priority < 0 {
		response.BadRequest(c, "priority must be >= 0")
		return
	}
	if req.RateMultiplier != nil && *req.RateMultiplier < 0 {
		response.BadRequest(c, "rate_multiplier must be >= 0")
		return
	}
	if req.LoadFactor != nil && *req.LoadFactor > 10000 {
		response.BadRequest(c, "load_factor must be <= 10000")
		return
	}

	entries, err := parseAntigravityOAuthImportEntries(req)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if len(entries) == 0 {
		response.BadRequest(c, "请输入 Antigravity OAuth JSON 或 refresh_token")
		return
	}

	executeAdminIdempotentJSON(c, "admin.accounts.import_antigravity_oauth", req, service.DefaultWriteIdempotencyTTL(), func(ctx context.Context) (any, error) {
		return h.importAntigravityOAuthAccounts(ctx, req, entries)
	})
}

func (h *AccountHandler) importAntigravityOAuthAccounts(ctx context.Context, req AntigravityOAuthImportRequest, entries []antigravityImportEntry) (AntigravityOAuthImportResult, error) {
	result := AntigravityOAuthImportResult{
		Total: len(entries),
		Items: make([]AntigravityOAuthImportItem, 0, len(entries)),
	}

	existingAccounts, err := h.listAccountsFiltered(ctx, service.PlatformAntigravity, service.AccountTypeOAuth, "", "", 0, "", "created_at", "desc")
	if err != nil {
		return result, err
	}
	index := buildAntigravityAccountIndex(existingAccounts)

	updateExisting := true
	if req.UpdateExisting != nil {
		updateExisting = *req.UpdateExisting
	}
	concurrency := 3
	if req.Concurrency != nil {
		concurrency = *req.Concurrency
	}
	priority := 50
	if req.Priority != nil {
		priority = *req.Priority
	}
	skipDefaultGroupBind := false
	if req.SkipDefaultGroupBind != nil {
		skipDefaultGroupBind = *req.SkipDefaultGroupBind
	}
	skipMixedChannelCheck := req.ConfirmMixedChannelRisk != nil && *req.ConfirmMixedChannelRisk
	fillProjectID := true
	if req.FillProjectID != nil {
		fillProjectID = *req.FillProjectID
	}

	seenIdentity := map[string]int{}
	for _, entry := range entries {
		item, err := h.normalizeAntigravityImportEntry(ctx, req, entry)
		if err != nil {
			result.Failed++
			result.Items = append(result.Items, AntigravityOAuthImportItem{
				Index:   entry.Index,
				Action:  "failed",
				Message: err.Error(),
			})
			result.Errors = append(result.Errors, AntigravityOAuthImportMessage{
				Index:   entry.Index,
				Message: err.Error(),
			})
			continue
		}

		accountName := buildAntigravityCreateAccountName(req.Name, item, entry.Index, len(entries))
		effectiveExpiresAt, autoPauseOnExpired, expiryWarnings, expiryErr := resolveAntigravityImportExpiry(req, item)
		if expiryErr != nil {
			result.Failed++
			result.Items = append(result.Items, AntigravityOAuthImportItem{
				Index:   entry.Index,
				Name:    accountName,
				Action:  "failed",
				Message: expiryErr.Error(),
			})
			result.Errors = append(result.Errors, AntigravityOAuthImportMessage{
				Index:   entry.Index,
				Name:    accountName,
				Message: expiryErr.Error(),
			})
			continue
		}
		for _, warning := range append(item.WarningTexts, expiryWarnings...) {
			result.Warnings = append(result.Warnings, AntigravityOAuthImportMessage{
				Index:   entry.Index,
				Name:    accountName,
				Message: warning,
			})
		}

		if duplicateIndex, ok := firstSeenAntigravityIdentity(seenIdentity, item.IdentityKeys); ok {
			message := fmt.Sprintf("与第 %d 条导入项重复，已跳过", duplicateIndex)
			result.Skipped++
			result.Items = append(result.Items, AntigravityOAuthImportItem{
				Index:   entry.Index,
				Name:    accountName,
				Action:  "skipped",
				Message: message,
			})
			result.Warnings = append(result.Warnings, AntigravityOAuthImportMessage{
				Index:   entry.Index,
				Name:    accountName,
				Message: message,
			})
			continue
		}
		markAntigravityIdentitySeen(seenIdentity, item.IdentityKeys, entry.Index)

		credentials := cloneAntigravityImportMap(item.Credentials)
		extra := mergeAntigravityImportMap(req.Extra, item.Extra)

		if fillProjectID && antigravityCredentialString(credentials, "project_id") == "" && h.antigravityOAuthService != nil {
			proxyURL, proxyErr := h.resolveAntigravityImportProxyURL(ctx, req.ProxyID)
			if proxyErr != nil {
				result.Failed++
				result.Items = append(result.Items, AntigravityOAuthImportItem{
					Index:   entry.Index,
					Name:    accountName,
					Action:  "failed",
					Message: proxyErr.Error(),
				})
				result.Errors = append(result.Errors, AntigravityOAuthImportMessage{
					Index:   entry.Index,
					Name:    accountName,
					Message: proxyErr.Error(),
				})
				continue
			}
			projectID, fillErr := h.antigravityOAuthService.FillProjectID(ctx, &service.Account{
				Platform: service.PlatformAntigravity,
				Type:     service.AccountTypeOAuth,
				ProxyID:  req.ProxyID,
			}, item.AccessToken)
			if fillErr != nil || strings.TrimSpace(projectID) == "" {
				message := "缺少 project_id 且自动补全失败"
				if fillErr != nil {
					message = fmt.Sprintf("缺少 project_id 且自动补全失败: %v", fillErr)
				}
				if proxyURL == "" && req.ProxyID != nil {
					message += "（请确认 proxy_id 可用且可访问 Google Code Assist）"
				}
				result.Failed++
				result.Items = append(result.Items, AntigravityOAuthImportItem{
					Index:   entry.Index,
					Name:    accountName,
					Action:  "failed",
					Message: message,
				})
				result.Errors = append(result.Errors, AntigravityOAuthImportMessage{
					Index:   entry.Index,
					Name:    accountName,
					Message: message,
				})
				continue
			}
			credentials["project_id"] = projectID
		}

		if antigravityCredentialString(credentials, "project_id") == "" {
			result.Failed++
			message := "缺少 project_id；可在请求中设置 fill_project_id=true 并配置可用 proxy"
			result.Items = append(result.Items, AntigravityOAuthImportItem{
				Index:   entry.Index,
				Name:    accountName,
				Action:  "failed",
				Message: message,
			})
			result.Errors = append(result.Errors, AntigravityOAuthImportMessage{
				Index:   entry.Index,
				Name:    accountName,
				Message: message,
			})
			continue
		}

		existing := index.Find(item.IdentityKeys)
		if existing != nil && updateExisting {
			preserveExistingRefresh := item.RefreshToken == "" &&
				antigravityCredentialString(existing.Credentials, "refresh_token") != ""
			if preserveExistingRefresh {
				result.Warnings = append(result.Warnings, AntigravityOAuthImportMessage{
					Index:   entry.Index,
					Name:    accountName,
					Message: "已有账号包含 refresh_token，本次 accessToken-only 导入已保留自动续期凭据",
				})
				effectiveExpiresAt = nil
				autoPauseOnExpired = nil
			}
			mergedCredentials := mergeAntigravityImportCredentials(existing.Credentials, credentials, item)
			mergedExtra := mergeAntigravityImportMap(existing.Extra, extra)
			updateInput := &service.UpdateAccountInput{
				Credentials:        mergedCredentials,
				Extra:              mergedExtra,
				Concurrency:        req.Concurrency,
				Priority:           req.Priority,
				RateMultiplier:     req.RateMultiplier,
				LoadFactor:         req.LoadFactor,
				ExpiresAt:          effectiveExpiresAt,
				AutoPauseOnExpired: autoPauseOnExpired,
			}
			if req.ProxyID != nil {
				updateInput.ProxyID = req.ProxyID
			}
			if len(req.GroupIDs) > 0 {
				groupIDs := append([]int64(nil), req.GroupIDs...)
				updateInput.GroupIDs = &groupIDs
				updateInput.SkipMixedChannelCheck = skipMixedChannelCheck
			}
			if item.Email != "" {
				email := item.Email
				updateInput.AccountEmail = &email
			}
			updated, updateErr := h.adminService.UpdateAccount(ctx, existing.ID, updateInput)
			if updateErr != nil {
				result.Failed++
				result.Items = append(result.Items, AntigravityOAuthImportItem{
					Index:   entry.Index,
					Name:    accountName,
					Action:  "failed",
					Message: updateErr.Error(),
				})
				result.Errors = append(result.Errors, AntigravityOAuthImportMessage{
					Index:   entry.Index,
					Name:    accountName,
					Message: updateErr.Error(),
				})
				continue
			}
			if h.tokenCacheInvalidator != nil && updated != nil {
				_ = h.tokenCacheInvalidator.InvalidateToken(ctx, updated)
			}
			result.Updated++
			accountID := existing.ID
			if updated != nil {
				accountID = updated.ID
				index.Add(*updated)
			}
			result.Items = append(result.Items, AntigravityOAuthImportItem{
				Index:     entry.Index,
				Name:      accountName,
				Action:    "updated",
				AccountID: accountID,
			})
			continue
		}

		createInput := &service.CreateAccountInput{
			Name:                  accountName,
			Notes:                 req.Notes,
			Platform:              service.PlatformAntigravity,
			Type:                  service.AccountTypeOAuth,
			Credentials:           credentials,
			Extra:                 extra,
			ProxyID:               req.ProxyID,
			Concurrency:           concurrency,
			Priority:              priority,
			RateMultiplier:        req.RateMultiplier,
			LoadFactor:            req.LoadFactor,
			GroupIDs:              req.GroupIDs,
			ExpiresAt:             effectiveExpiresAt,
			AutoPauseOnExpired:    autoPauseOnExpired,
			SkipDefaultGroupBind:  skipDefaultGroupBind,
			SkipMixedChannelCheck: skipMixedChannelCheck,
			AccountEmail:          item.Email,
		}
		account, createErr := h.adminService.CreateAccount(ctx, createInput)
		if createErr != nil {
			result.Failed++
			result.Items = append(result.Items, AntigravityOAuthImportItem{
				Index:   entry.Index,
				Name:    accountName,
				Action:  "failed",
				Message: createErr.Error(),
			})
			result.Errors = append(result.Errors, AntigravityOAuthImportMessage{
				Index:   entry.Index,
				Name:    accountName,
				Message: createErr.Error(),
			})
			continue
		}
		if account != nil {
			h.adminService.ForceAntigravityPrivacy(ctx, account)
			index.Add(*account)
		}
		result.Created++
		accountID := int64(0)
		if account != nil {
			accountID = account.ID
		}
		result.Items = append(result.Items, AntigravityOAuthImportItem{
			Index:     entry.Index,
			Name:      accountName,
			Action:    "created",
			AccountID: accountID,
		})
	}

	return result, nil
}

func parseAntigravityOAuthImportEntries(req AntigravityOAuthImportRequest) ([]antigravityImportEntry, error) {
	contents := make([]string, 0, 1+len(req.Contents))
	if strings.TrimSpace(req.Content) != "" {
		contents = append(contents, req.Content)
	}
	for _, content := range req.Contents {
		if strings.TrimSpace(content) != "" {
			contents = append(contents, content)
		}
	}

	var entries []antigravityImportEntry
	for _, content := range contents {
		values, err := parseAntigravityOAuthImportContent(content)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			entries = append(entries, antigravityImportEntry{
				Index: len(entries) + 1,
				Value: value,
			})
		}
	}
	return entries, nil
}

func parseAntigravityOAuthImportContent(content string) ([]any, error) {
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return nil, nil
	}
	if looksLikeJSON(trimmed) {
		values, err := decodeCodexJSONStream(trimmed)
		if err != nil {
			if strings.Contains(trimmed, "\n") {
				if lineValues, lineErr := parseAntigravityOAuthImportLines(trimmed); lineErr == nil {
					return lineValues, nil
				}
			}
			return nil, fmt.Errorf("JSON 解析失败: %w", err)
		}
		return flattenCodexImportValues(values), nil
	}
	return parseAntigravityOAuthImportLines(trimmed)
}

func parseAntigravityOAuthImportLines(content string) ([]any, error) {
	values := make([]any, 0)
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if looksLikeJSON(line) {
			lineValues, err := decodeCodexJSONStream(line)
			if err != nil {
				return nil, fmt.Errorf("第 %d 行 JSON 解析失败: %w", len(values)+1, err)
			}
			values = append(values, flattenCodexImportValues(lineValues)...)
			continue
		}
		values = append(values, line)
	}
	return values, nil
}

func (h *AccountHandler) normalizeAntigravityImportEntry(ctx context.Context, req AntigravityOAuthImportRequest, entry antigravityImportEntry) (*antigravityImportAccount, error) {
	now := time.Now().UTC()
	item := &antigravityImportAccount{
		Credentials: map[string]any{},
		Extra: map[string]any{
			"import_source": "antigravity_oauth",
			"imported_at":   now.Format(time.RFC3339),
		},
	}

	switch raw := entry.Value.(type) {
	case string:
		refreshToken := strings.TrimSpace(raw)
		if refreshToken == "" {
			return nil, errors.New("refresh_token 不能为空")
		}
		if h.antigravityOAuthService == nil {
			return nil, errors.New("仅 refresh_token 导入需要 Antigravity OAuth 服务")
		}
		tokenInfo, err := h.antigravityOAuthService.ValidateRefreshToken(ctx, refreshToken, req.ProxyID)
		if err != nil {
			return nil, fmt.Errorf("refresh_token 校验失败: %w", err)
		}
		return antigravityImportAccountFromTokenInfo(h.antigravityOAuthService, item, tokenInfo, refreshToken, entry.Index, now)
	case map[string]any:
		return normalizeAntigravityImportObject(ctx, h, req, item, raw, entry.Index, now)
	default:
		return nil, fmt.Errorf("第 %d 条格式不支持", entry.Index)
	}
}

func normalizeAntigravityImportObject(ctx context.Context, h *AccountHandler, req AntigravityOAuthImportRequest, item *antigravityImportAccount, raw map[string]any, index int, now time.Time) (*antigravityImportAccount, error) {
	item.AccessToken = firstCodexString(raw,
		[]string{"access_token"},
		[]string{"accessToken"},
		[]string{"token"},
	)
	item.RefreshToken = firstCodexString(raw,
		[]string{"refresh_token"},
		[]string{"refreshToken"},
	)
	item.TokenType = firstCodexString(raw, []string{"token_type"}, []string{"tokenType"})
	item.Email = firstCodexString(raw, []string{"email"})
	item.ProjectID = firstCodexString(raw, []string{"project_id"}, []string{"projectId"})

	if item.AccessToken == "" && item.RefreshToken != "" {
		if h.antigravityOAuthService == nil {
			return nil, errors.New("仅 refresh_token 导入需要 Antigravity OAuth 服务")
		}
		tokenInfo, err := h.antigravityOAuthService.ValidateRefreshToken(ctx, item.RefreshToken, req.ProxyID)
		if err != nil {
			return nil, fmt.Errorf("refresh_token 校验失败: %w", err)
		}
		return antigravityImportAccountFromTokenInfo(h.antigravityOAuthService, item, tokenInfo, item.RefreshToken, index, now)
	}
	if item.AccessToken == "" {
		return nil, errors.New("缺少 access_token")
	}

	expiresAtUnix, expiryWarnings, expiryErr := resolveAntigravityCredentialExpiry(raw, now)
	if expiryErr != nil {
		return nil, expiryErr
	}
	item.ExpiresAtUnix = expiresAtUnix
	item.WarningTexts = append(item.WarningTexts, expiryWarnings...)

	item.Credentials["access_token"] = item.AccessToken
	if item.RefreshToken != "" {
		item.Credentials["refresh_token"] = item.RefreshToken
	}
	if item.TokenType != "" {
		item.Credentials["token_type"] = item.TokenType
	} else {
		item.Credentials["token_type"] = "Bearer"
	}
	if item.Email != "" {
		item.Credentials["email"] = item.Email
	}
	if item.ProjectID != "" {
		item.Credentials["project_id"] = item.ProjectID
	}
	if expiresAtUnix > 0 {
		item.Credentials["expires_at"] = strconv.FormatInt(expiresAtUnix, 10)
	}

	item.Extra["access_token_sha256"] = antigravityTokenFingerprint(item.AccessToken)
	item.IdentityKeys = buildAntigravityImportIdentityKeys(item.Email, item.AccessToken, item.RefreshToken)
	item.Name = buildAntigravityImportAccountName(item, index)
	return item, nil
}

func antigravityImportAccountFromTokenInfo(oauthService *service.AntigravityOAuthService, item *antigravityImportAccount, tokenInfo *service.AntigravityTokenInfo, fallbackRefreshToken string, index int, now time.Time) (*antigravityImportAccount, error) {
	if tokenInfo == nil || strings.TrimSpace(tokenInfo.AccessToken) == "" {
		return nil, errors.New("refresh_token 校验未返回 access_token")
	}
	if tokenInfo.ExpiresAt > 0 && now.Unix() > tokenInfo.ExpiresAt+antigravityImportClockSkewSeconds {
		return nil, fmt.Errorf("access_token 已过期: %s", time.Unix(tokenInfo.ExpiresAt, 0).UTC().Format(time.RFC3339))
	}

	credentials := map[string]any{}
	if oauthService != nil {
		credentials = oauthService.BuildAccountCredentials(tokenInfo)
	} else {
		credentials["access_token"] = tokenInfo.AccessToken
		if tokenInfo.RefreshToken != "" {
			credentials["refresh_token"] = tokenInfo.RefreshToken
		} else if strings.TrimSpace(fallbackRefreshToken) != "" {
			credentials["refresh_token"] = strings.TrimSpace(fallbackRefreshToken)
		}
		if tokenInfo.ExpiresAt > 0 {
			credentials["expires_at"] = strconv.FormatInt(tokenInfo.ExpiresAt, 10)
		}
		if tokenInfo.Email != "" {
			credentials["email"] = tokenInfo.Email
		}
		if tokenInfo.ProjectID != "" {
			credentials["project_id"] = tokenInfo.ProjectID
		}
		if tokenInfo.TokenType != "" {
			credentials["token_type"] = tokenInfo.TokenType
		}
	}

	item.AccessToken = tokenInfo.AccessToken
	item.RefreshToken = antigravityCredentialString(credentials, "refresh_token")
	item.Email = antigravityCredentialString(credentials, "email")
	item.ProjectID = antigravityCredentialString(credentials, "project_id")
	item.Credentials = credentials
	if tokenInfo.ProjectIDMissing {
		item.WarningTexts = append(item.WarningTexts, "refresh_token 校验成功但未获取 project_id，导入时会尝试自动补全")
	}
	item.Extra["access_token_sha256"] = antigravityTokenFingerprint(item.AccessToken)
	item.IdentityKeys = buildAntigravityImportIdentityKeys(item.Email, item.AccessToken, item.RefreshToken)
	item.Name = buildAntigravityImportAccountName(item, index)
	return item, nil
}

func resolveAntigravityCredentialExpiry(raw map[string]any, now time.Time) (int64, []string, error) {
	warnings := make([]string, 0, 2)
	if expiresAt, ok := firstCodexTime(raw, []string{"expires_at"}, []string{"expiresAt"}); ok {
		expiresAtUnix := expiresAt.Unix()
		if now.Unix() > expiresAtUnix+antigravityImportClockSkewSeconds {
			return 0, nil, fmt.Errorf("access_token 已过期: %s", expiresAt.UTC().Format(time.RFC3339))
		}
		return expiresAtUnix, warnings, nil
	}
	if expiredAt, ok := firstCodexTime(raw, []string{"expired"}, []string{"expires"}); ok {
		expiresAtUnix := expiredAt.Unix() - 300
		if now.Unix() > expiresAtUnix+antigravityImportClockSkewSeconds {
			return 0, nil, fmt.Errorf("access_token 已过期: %s", expiredAt.UTC().Format(time.RFC3339))
		}
		return expiresAtUnix, warnings, nil
	}
	if issuedAt, ok := codexTimeAt(raw, []string{"timestamp"}); ok {
		expiresIn := int64(0)
		if value, ok := codexPathValue(raw, []string{"expires_in"}); ok {
			expiresIn = antigravityInt64Value(value)
		}
		if expiresIn <= 0 {
			if value, ok := codexPathValue(raw, []string{"expiresIn"}); ok {
				expiresIn = antigravityInt64Value(value)
			}
		}
		if expiresIn > 0 {
			expiresAtUnix := issuedAt.Unix() + expiresIn - 300
			if now.Unix() > expiresAtUnix+antigravityImportClockSkewSeconds {
				return 0, nil, fmt.Errorf("access_token 已过期: %s", time.Unix(expiresAtUnix, 0).UTC().Format(time.RFC3339))
			}
			return expiresAtUnix, warnings, nil
		}
	}
	if refreshToken := firstCodexString(raw, []string{"refresh_token"}, []string{"refreshToken"}); refreshToken != "" {
		warnings = append(warnings, "未解析 access_token 过期时间，导入后依赖 refresh_token 自动续期")
		return 0, warnings, nil
	}
	warnings = append(warnings, "无法解析 access_token 过期时间，导入后需自行确认令牌有效性")
	return 0, warnings, nil
}

func resolveAntigravityImportExpiry(req AntigravityOAuthImportRequest, item *antigravityImportAccount) (*int64, *bool, []string, error) {
	if item == nil {
		return nil, nil, nil, errors.New("导入项为空")
	}
	var requestExpiresAt *time.Time
	if req.ExpiresAt != nil && *req.ExpiresAt > 0 {
		t := time.Unix(*req.ExpiresAt, 0).UTC()
		requestExpiresAt = &t
	}

	if strings.TrimSpace(item.RefreshToken) == "" {
		var accountExpiresAt *time.Time
		if item.ExpiresAtUnix > 0 {
			tokenExpiresAt := time.Unix(item.ExpiresAtUnix, 0).UTC()
			accountExpiresAt = &tokenExpiresAt
		}
		if requestExpiresAt != nil {
			accountExpiresAt = earlierAntigravityTime(accountExpiresAt, requestExpiresAt)
		}
		if accountExpiresAt == nil {
			return nil, nil, nil, errors.New("未包含 refresh_token，且无法解析 access_token 过期时间；请在请求中设置 expires_at")
		}
		if accountExpiresAt.Unix() <= time.Now().UTC().Unix()-antigravityImportClockSkewSeconds {
			return nil, nil, nil, fmt.Errorf("过期时间已过期: %s", accountExpiresAt.Format(time.RFC3339))
		}
		warnings := []string{"未包含 refresh_token，已按 accessToken/账号过期时间设置自动停止调度"}
		if req.AutoPauseOnExpired != nil && !*req.AutoPauseOnExpired {
			warnings = append(warnings, "未包含 refresh_token，已强制开启过期自动暂停")
		}
		autoPause := true
		expiresAtUnix := accountExpiresAt.Unix()
		return &expiresAtUnix, &autoPause, warnings, nil
	}

	if requestExpiresAt != nil {
		expiresAtUnix := requestExpiresAt.Unix()
		return &expiresAtUnix, req.AutoPauseOnExpired, nil, nil
	}
	return nil, req.AutoPauseOnExpired, nil, nil
}

func buildAntigravityImportAccountName(item *antigravityImportAccount, index int) string {
	for _, candidate := range []string{item.Email, item.ProjectID} {
		candidate = strings.TrimSpace(candidate)
		if candidate != "" {
			return candidate
		}
	}
	return fmt.Sprintf("Antigravity 导入账号 %d", index)
}

func buildAntigravityCreateAccountName(base string, item *antigravityImportAccount, index, total int) string {
	base = strings.TrimSpace(base)
	if base == "" {
		if item == nil {
			return fmt.Sprintf("Antigravity 导入账号 %d", index)
		}
		return item.Name
	}
	if total > 1 {
		return fmt.Sprintf("%s #%d", base, index)
	}
	return base
}

func buildAntigravityImportIdentityKeys(email, accessToken, refreshToken string) []string {
	keys := make([]string, 0, 3)
	refreshToken = strings.TrimSpace(refreshToken)
	accessToken = strings.TrimSpace(accessToken)
	email = strings.ToLower(strings.TrimSpace(email))
	if refreshToken != "" {
		keys = append(keys, "refresh:"+antigravityTokenFingerprint(refreshToken))
	}
	if email != "" {
		keys = append(keys, "email:"+email)
	}
	if accessToken != "" {
		keys = append(keys, "access:"+antigravityTokenFingerprint(accessToken))
	}
	return keys
}

func buildAntigravityStoredIdentityKeys(email, accessToken, refreshToken string) []string {
	return buildAntigravityImportIdentityKeys(email, accessToken, refreshToken)
}

func buildAntigravityAccountIndex(accounts []service.Account) *antigravityAccountIndex {
	index := &antigravityAccountIndex{accountsByKey: map[string][]service.Account{}}
	for _, account := range accounts {
		index.Add(account)
	}
	return index
}

func (i *antigravityAccountIndex) Add(account service.Account) {
	if i == nil {
		return
	}
	if i.accountsByKey == nil {
		i.accountsByKey = map[string][]service.Account{}
	}
	i.remove(account.ID)
	keys := buildAntigravityStoredIdentityKeys(
		antigravityCredentialString(account.Credentials, "email"),
		antigravityCredentialString(account.Credentials, "access_token"),
		antigravityCredentialString(account.Credentials, "refresh_token"),
	)
	for _, key := range keys {
		i.accountsByKey[key] = upsertAntigravityAccount(i.accountsByKey[key], account)
	}
}

func (i *antigravityAccountIndex) remove(accountID int64) {
	for key, accounts := range i.accountsByKey {
		kept := accounts[:0]
		for _, account := range accounts {
			if account.ID != accountID {
				kept = append(kept, account)
			}
		}
		if len(kept) == 0 {
			delete(i.accountsByKey, key)
			continue
		}
		i.accountsByKey[key] = kept
	}
}

func upsertAntigravityAccount(accounts []service.Account, account service.Account) []service.Account {
	for idx := range accounts {
		if accounts[idx].ID == account.ID {
			accounts[idx] = account
			return accounts
		}
	}
	return append(accounts, account)
}

func (i *antigravityAccountIndex) Find(keys []string) *service.Account {
	if i == nil {
		return nil
	}
	for _, key := range keys {
		if len(i.accountsByKey[key]) > 0 {
			return &i.accountsByKey[key][0]
		}
	}
	return nil
}

func firstSeenAntigravityIdentity(seen map[string]int, keys []string) (int, bool) {
	for _, key := range keys {
		if index, ok := seen[key]; ok {
			return index, true
		}
	}
	return 0, false
}

func markAntigravityIdentitySeen(seen map[string]int, keys []string, index int) {
	for _, key := range keys {
		seen[key] = index
	}
}

func mergeAntigravityImportMap(existing, incoming map[string]any) map[string]any {
	out := make(map[string]any, len(existing)+len(incoming))
	for k, v := range existing {
		out[k] = v
	}
	for k, v := range incoming {
		out[k] = v
	}
	return out
}

func mergeAntigravityImportCredentials(existing, incoming map[string]any, item *antigravityImportAccount) map[string]any {
	out := mergeAntigravityImportMap(existing, incoming)
	if item == nil {
		return out
	}
	if strings.TrimSpace(item.RefreshToken) == "" {
		if antigravityCredentialString(existing, "refresh_token") == "" {
			delete(out, "refresh_token")
		} else {
			out["refresh_token"] = existing["refresh_token"]
		}
	}
	return out
}

func cloneAntigravityImportMap(input map[string]any) map[string]any {
	if len(input) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func antigravityCredentialString(credentials map[string]any, key string) string {
	if credentials == nil {
		return ""
	}
	return codexStringValue(credentials[key])
}

func antigravityTokenFingerprint(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func earlierAntigravityTime(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	if current == nil || candidate.Before(*current) {
		t := candidate.UTC()
		return &t
	}
	t := current.UTC()
	return &t
}

func antigravityInt64Value(value any) int64 {
	switch v := value.(type) {
	case json.Number:
		n, _ := v.Int64()
		return n
	case float64:
		return int64(v)
	case float32:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case int32:
		return int64(v)
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil {
			return n
		}
	}
	return 0
}

func (h *AccountHandler) resolveAntigravityImportProxyURL(ctx context.Context, proxyID *int64) (string, error) {
	if proxyID == nil || h.adminService == nil {
		return "", nil
	}
	proxy, err := h.adminService.GetProxy(ctx, *proxyID)
	if err != nil {
		return "", fmt.Errorf("proxy_id 无效: %w", err)
	}
	if proxy == nil {
		return "", errors.New("proxy_id 无效")
	}
	return proxy.URL(), nil
}
