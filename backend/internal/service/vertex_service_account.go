package service

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	newapiconstant "github.com/QuantumNous/new-api/constant"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyurl"
	"github.com/Wei-Shaw/sub2api/internal/pkg/proxyutil"
	"github.com/Wei-Shaw/sub2api/internal/pkg/servertiming"
	"github.com/golang-jwt/jwt/v5"
)

const (
	vertexDefaultLocation         = "us-central1"
	vertexGlobalLocation          = "global"
	vertexDefaultTokenURL         = "https://oauth2.googleapis.com/token"
	vertexCloudPlatformScope      = "https://www.googleapis.com/auth/cloud-platform"
	vertexServiceAccountCacheSkew = 5 * time.Minute
	vertexLockWaitTime            = 200 * time.Millisecond
	vertexAnthropicVersion        = "vertex-2023-10-16"
	vertexGlobalHost              = "aiplatform.googleapis.com"
	vertexAuthModeAPIKey          = "apikey"
)

var (
	vertexLocationPattern                = regexp.MustCompile(`^[a-z0-9-]+$`)
	vertexAnthropicDatedModelIDPattern   = regexp.MustCompile(`^(.+)-([0-9]{8})$`)
	vertexAnthropicAlreadyDatedIDPattern = regexp.MustCompile(`^.+@[0-9]{8}$`)
)

// vertexServiceAccountTokenURL 是换取 SA access token 的端点，测试中替换为本地服务。
var vertexServiceAccountTokenURL = vertexDefaultTokenURL

type vertexServiceAccountKey struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
}

type vertexTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int64  `json:"expires_in"`
	Error       string `json:"error"`
	ErrorDesc   string `json:"error_description"`
}

func (a *Account) IsVertexServiceAccount() bool {
	return a != nil && a.Type == AccountTypeServiceAccount
}

// IsNewAPIVertexServiceAccount identifies the exact persisted account shape
// that may serve native Gemini requests through Vertex. Keeping channel and
// platform checks here prevents unrelated newapi service accounts from gaining
// Google credential or endpoint behavior.
func (a *Account) IsNewAPIVertexServiceAccount() bool {
	return a != nil &&
		a.Platform == PlatformNewAPI &&
		a.ChannelType == newapiconstant.ChannelTypeVertexAi &&
		a.IsVertexServiceAccount()
}

func (a *Account) VertexProjectID() string {
	if a == nil {
		return ""
	}
	if v := strings.TrimSpace(a.GetCredential("project_id")); v != "" {
		return v
	}
	key, err := parseVertexServiceAccountKey(a)
	if err == nil {
		return strings.TrimSpace(key.ProjectID)
	}
	return ""
}

func (a *Account) VertexLocation(model string) string {
	if a == nil {
		return vertexDefaultLocation
	}
	if model != "" && a.Credentials != nil {
		if raw, ok := a.Credentials["vertex_model_locations"].(map[string]any); ok {
			if loc, ok := raw[model].(string); ok && strings.TrimSpace(loc) != "" {
				return strings.TrimSpace(loc)
			}
		}
	}
	// 可用性是硬约束，账号配置在可用性之内生效。两个方向对称：配了区域但模型
	// 只在 global（照发必 404）→ 走 global；配了 global 但模型没有 global 列
	// （照发必 404）→ 拉回区域。两者都不成立时才听账号配置。
	//
	// 想精确指定某模型的区域时用 vertex_model_locations（已在上面优先返回），
	// 那是逐模型的显式覆盖，优先于这里的推断。
	if vertexModelUsesGlobalLocation(model) {
		return vertexGlobalLocation
	}
	configured := strings.TrimSpace(a.GetCredential("location"))
	if configured == "" {
		configured = strings.TrimSpace(a.GetCredential("vertex_location"))
	}
	if configured != "" {
		if configured == vertexGlobalLocation && !vertexModelSupportsGlobal(model) {
			return vertexModelRegionalFallback(model)
		}
		return configured
	}
	return vertexModelRegionalFallback(model)
}

// vertexModelLocationSupport 是按模型的 Vertex 可用性表，取代原先那张只能
// 「把模型推向 global」的单向清单。两处结构缺陷促成这次重做：
//
//  1. 单向。表里只能表达「该走 global」，无法表达「不支持 global」。而官方
//     文档明确 veo-3.1-* 只在 us-central1、gemini-embedding-001 完全没有
//     Global 列。现网唯一活账号（id=74）账号级就是 location=global，于是这
//     两类模型会被直发 global，必 404。
//  2. 旧清单内容本身是错的。官方位置文档（docs.cloud.google.com/vertex-ai/
//     generative-ai/docs/learn/locations，2026-10-22 核对）显示旧清单那 5 个
//     模型全都支持 Global + us + eu，没有一个是 global-only；而它的优先级
//     高于账号 location，等于无条件剥夺了运维选区能力。
//
// globalOnly 为 true 表示该模型只在全局端点提供（账号即使配了区域也必须走
// global）；globalSupported 为 false 表示该模型在全局端点不存在（账号配了
// global 也必须拉回区域）。两者不能同真。regional 给出该模型的区域兜底。
type vertexModelLocationSupport struct {
	globalOnly      bool
	globalSupported bool
	regional        string
}

// 未列出的模型：既支持 global 也支持区域，按账号配置或默认区域走。
var vertexModelLocations = map[string]vertexModelLocationSupport{
	// 官方只有 Global 一列 —— 账号配了区域也要走 global，否则 404。
	// gemini-3-flash-preview 曾被 #2822 从 global 侧删掉以迁合一个上游测试
	// （那个测试守的是「API Key + project_id 走项目端点」这个认证边界，拿
	// global-only 模型当夹具本就是夹具选错），这里按官方恢复。
	"gemini-3-flash-preview": {globalOnly: true, globalSupported: true},
	"gemini-3.1-pro-preview": {globalOnly: true, globalSupported: true},
	"gemini-3-pro-image":     {globalOnly: true, globalSupported: true},

	// 官方没有 Global 列 —— 账号配了 global 也要拉回区域。
	"gemini-embedding-001":      {globalSupported: false, regional: vertexDefaultLocation},
	"veo-3.1-generate-001":      {globalSupported: false, regional: "us-central1"},
	"veo-3.1-fast-generate-001": {globalSupported: false, regional: "us-central1"},
	"veo-3.1-lite-generate-001": {globalSupported: false, regional: "us-central1"},
}

func vertexNormalizeModelID(model string) string {
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(model), "models/"))
}

// vertexModelUsesGlobalLocation reports whether the model is only served from
// the global endpoint, so it must go there even when the account names a region.
func vertexModelUsesGlobalLocation(model string) bool {
	return vertexModelLocations[vertexNormalizeModelID(model)].globalOnly
}

// vertexModelSupportsGlobal reports whether the global endpoint serves the
// model at all. Unlisted models are assumed to support it (that is the common
// case); only models the official docs show without a Global column are listed.
func vertexModelSupportsGlobal(model string) bool {
	entry, ok := vertexModelLocations[vertexNormalizeModelID(model)]
	if !ok {
		return true
	}
	return entry.globalSupported
}

// vertexModelRegionalFallback gives the region to use when global is not an
// option for the model.
func vertexModelRegionalFallback(model string) string {
	if entry, ok := vertexModelLocations[vertexNormalizeModelID(model)]; ok && entry.regional != "" {
		return entry.regional
	}
	return vertexDefaultLocation
}

// VertexServiceAccountJSON returns the raw service-account JSON string, whether the
// credential is stored as a JSON string or a nested object. Empty when absent. The
// newapi bridge forwards this as the channel key so new-api's Vertex adaptor can
// unmarshal it (project_id is derived from the JSON, not passed separately).
func (a *Account) VertexServiceAccountJSON() string {
	if a == nil || a.Credentials == nil {
		return ""
	}
	if raw := strings.TrimSpace(a.GetCredential("service_account_json")); raw != "" {
		return raw
	}
	if raw := strings.TrimSpace(a.GetCredential("service_account")); raw != "" {
		return raw
	}
	if nested, ok := a.Credentials["service_account_json"].(map[string]any); ok {
		if b, err := json.Marshal(nested); err == nil {
			return string(b)
		}
	}
	if nested, ok := a.Credentials["service_account"].(map[string]any); ok {
		if b, err := json.Marshal(nested); err == nil {
			return string(b)
		}
	}
	return ""
}

func parseVertexServiceAccountKey(account *Account) (*vertexServiceAccountKey, error) {
	if account == nil || account.Credentials == nil {
		return nil, errors.New("service account credentials not configured")
	}

	if raw := strings.TrimSpace(account.GetCredential("service_account_json")); raw != "" {
		return parseVertexServiceAccountJSON([]byte(raw))
	}
	if raw := strings.TrimSpace(account.GetCredential("service_account")); raw != "" {
		return parseVertexServiceAccountJSON([]byte(raw))
	}
	if nested, ok := account.Credentials["service_account_json"].(map[string]any); ok {
		b, _ := json.Marshal(nested)
		return parseVertexServiceAccountJSON(b)
	}
	if nested, ok := account.Credentials["service_account"].(map[string]any); ok {
		b, _ := json.Marshal(nested)
		return parseVertexServiceAccountJSON(b)
	}
	return nil, errors.New("service_account_json not found in credentials")
}

func parseVertexServiceAccountJSON(raw []byte) (*vertexServiceAccountKey, error) {
	var key vertexServiceAccountKey
	if err := json.Unmarshal(raw, &key); err != nil {
		return nil, fmt.Errorf("invalid service account json: %w", err)
	}
	if strings.TrimSpace(key.ClientEmail) == "" {
		return nil, errors.New("service account json missing client_email")
	}
	if strings.TrimSpace(key.PrivateKey) == "" {
		return nil, errors.New("service account json missing private_key")
	}
	if strings.TrimSpace(key.ProjectID) == "" {
		return nil, errors.New("service account json missing project_id")
	}
	// Prefer the package-level URL (tests override it) over any crafted token_uri
	// in the SA JSON — still SSRF-safe when left at the Google default.
	key.TokenURI = vertexServiceAccountTokenURL
	return &key, nil
}

func vertexServiceAccountCacheKey(account *Account, key *vertexServiceAccountKey) string {
	fingerprint := ""
	if key != nil {
		sum := sha256.Sum256([]byte(key.ClientEmail + "\x00" + key.PrivateKeyID))
		fingerprint = hex.EncodeToString(sum[:8])
	}
	if fingerprint == "" && account != nil {
		fingerprint = fmt.Sprintf("account:%d", account.ID)
	}
	return "vertex:service_account:" + fingerprint
}

// getVertexServiceAccountAccessToken obtains an access token for a Vertex service account,
// using the shared cache and distributed lock to avoid redundant exchanges.
func getVertexServiceAccountAccessToken(ctx context.Context, cache GeminiTokenCache, account *Account) (string, error) {
	key, err := parseVertexServiceAccountKey(account)
	if err != nil {
		return "", err
	}
	cacheKey := vertexServiceAccountCacheKey(account, key)

	if cache != nil {
		if token, err := cache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
			return token, nil
		}
	}

	locked := false
	if cache != nil {
		var lockErr error
		locked, lockErr = cache.AcquireRefreshLock(ctx, cacheKey, 30*time.Second)
		if lockErr == nil && locked {
			defer func() { _ = cache.ReleaseRefreshLock(ctx, cacheKey) }()
		} else if lockErr != nil {
			slog.Warn("vertex_service_account_token_lock_failed", "account_id", account.ID, "error", lockErr)
		} else {
			time.Sleep(vertexLockWaitTime)
			if token, err := cache.GetAccessToken(ctx, cacheKey); err == nil && strings.TrimSpace(token) != "" {
				return token, nil
			}
		}
	}

	accessToken, ttl, err := exchangeVertexServiceAccountToken(ctx, key, vertexServiceAccountProxyURL(account))
	if err != nil {
		return "", err
	}
	if cache != nil {
		_ = cache.SetAccessToken(ctx, cacheKey, accessToken, ttl)
	}
	return accessToken, nil
}

func vertexServiceAccountProxyURL(account *Account) string {
	if account == nil || account.ProxyID == nil || account.Proxy == nil {
		return ""
	}
	return account.Proxy.URL()
}

func newVertexServiceAccountHTTPClient(proxyURL string) (*http.Client, error) {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return servertiming.InstrumentClient(&http.Client{Timeout: 15 * time.Second}), nil
	}

	_, parsedProxy, err := proxyurl.Parse(proxyURL)
	if err != nil {
		return nil, err
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil, fmt.Errorf("unexpected default transport type %T", http.DefaultTransport)
	}
	transport := defaultTransport.Clone()
	transport.Proxy = nil
	if err := proxyutil.ConfigureTransportProxy(transport, parsedProxy); err != nil {
		return nil, err
	}
	return servertiming.InstrumentClient(&http.Client{Timeout: 15 * time.Second, Transport: transport}), nil
}

func exchangeVertexServiceAccountToken(ctx context.Context, key *vertexServiceAccountKey, proxyURL string) (string, time.Duration, error) {
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   key.ClientEmail,
		"scope": vertexCloudPlatformScope,
		"aud":   key.TokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	if strings.TrimSpace(key.PrivateKeyID) != "" {
		token.Header["kid"] = key.PrivateKeyID
	}
	privateKey, err := jwt.ParseRSAPrivateKeyFromPEM([]byte(key.PrivateKey))
	if err != nil {
		return "", 0, fmt.Errorf("parse service account private key: %w", err)
	}
	assertion, err := token.SignedString(privateKey)
	if err != nil {
		return "", 0, fmt.Errorf("sign service account assertion: %w", err)
	}

	values := url.Values{}
	values.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	values.Set("assertion", assertion)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, key.TokenURI, strings.NewReader(values.Encode()))
	if err != nil {
		return "", 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client, err := newVertexServiceAccountHTTPClient(proxyURL)
	if err != nil {
		return "", 0, fmt.Errorf("configure service account token proxy: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("service account token request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var parsed vertexTokenResponse
	_ = json.Unmarshal(body, &parsed)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := strings.TrimSpace(parsed.ErrorDesc)
		if msg == "" {
			msg = strings.TrimSpace(parsed.Error)
		}
		if msg == "" {
			msg = string(bytes.TrimSpace(body))
		}
		return "", 0, &vertexTokenRequestError{
			StatusCode:  resp.StatusCode,
			Code:        strings.TrimSpace(parsed.Error),
			Description: strings.TrimSpace(parsed.ErrorDesc),
			Message:     msg,
		}
	}
	if strings.TrimSpace(parsed.AccessToken) == "" {
		return "", 0, errors.New("service account token response missing access_token")
	}
	ttl := time.Duration(parsed.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	if ttl > vertexServiceAccountCacheSkew {
		ttl -= vertexServiceAccountCacheSkew
	}
	return parsed.AccessToken, ttl, nil
}

// buildVertexGeminiAccountURL 按账号构造 Vertex Gemini 请求地址：未配置 project_id 的 API Key 账号
// 走 Express mode 全局端点，其余账号走项目级端点。
func buildVertexGeminiAccountURL(account *Account, model, action string, stream bool) (string, error) {
	if account.IsVertexAPIKey() && account.VertexProjectID() == "" {
		return buildVertexGeminiExpressURL(model, action, stream)
	}
	return buildVertexGeminiURL(account.VertexProjectID(), account.VertexLocation(model), model, action, stream)
}

// buildVertexGeminiExpressURL 构造不带项目与区域的全局端点地址，仅 API Key 认证可用。
func buildVertexGeminiExpressURL(model, action string, stream bool) (string, error) {
	model = strings.TrimSpace(model)
	action = strings.TrimSpace(action)
	if model == "" {
		return "", errors.New("vertex model is required")
	}
	if err := validateVertexGeminiAction(action); err != nil {
		return "", err
	}
	u := fmt.Sprintf("https://%s/v1/publishers/google/models/%s:%s", vertexGlobalHost, url.PathEscape(model), action)
	if stream {
		u += "?alt=sse"
	}
	return u, nil
}

func validateVertexGeminiAction(action string) error {
	switch action {
	case "generateContent", "streamGenerateContent", "countTokens":
		return nil
	default:
		return fmt.Errorf("unsupported vertex gemini action: %s", action)
	}
}

// setVertexGeminiAuth 给 Vertex Gemini 请求设置认证头：API Key 账号用 x-goog-api-key，
// Service Account JSON 账号用换取的 Bearer access token。
func setVertexGeminiAuth(ctx context.Context, req *http.Request, account *Account, tokenProvider *GeminiTokenProvider) error {
	if account.IsVertexAPIKey() {
		apiKey := strings.TrimSpace(account.GetCredential("api_key"))
		if apiKey == "" {
			return errors.New("vertex api_key not configured")
		}
		req.Header.Set("x-goog-api-key", apiKey)
		return nil
	}
	if tokenProvider == nil {
		return errors.New("gemini token provider not configured")
	}
	accessToken, err := tokenProvider.GetAccessToken(ctx, account)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	return nil
}

func buildVertexGeminiURL(projectID, location, model, action string, stream bool) (string, error) {
	projectID = strings.TrimSpace(projectID)
	location = strings.TrimSpace(location)
	model = strings.TrimSpace(model)
	action = strings.TrimSpace(action)
	if projectID == "" {
		return "", errors.New("vertex project_id is required")
	}
	if location == "" {
		location = vertexDefaultLocation
	}
	if !vertexLocationPattern.MatchString(location) {
		return "", fmt.Errorf("invalid vertex location: %s", location)
	}
	if model == "" {
		return "", errors.New("vertex model is required")
	}
	if err := validateVertexGeminiAction(action); err != nil {
		return "", err
	}
	host := fmt.Sprintf("%s-aiplatform.googleapis.com", location)
	if location == "global" {
		host = vertexGlobalHost
	}
	u := fmt.Sprintf(
		"https://%s/v1/projects/%s/locations/%s/publishers/google/models/%s:%s",
		host,
		url.PathEscape(projectID),
		url.PathEscape(location),
		url.PathEscape(model),
		action,
	)
	if stream {
		u += "?alt=sse"
	}
	return u, nil
}

func buildVertexAnthropicURL(projectID, location, model string, stream bool) (string, error) {
	projectID = strings.TrimSpace(projectID)
	location = strings.TrimSpace(location)
	model = strings.TrimSpace(model)
	if projectID == "" {
		return "", errors.New("vertex project_id is required")
	}
	if location == "" {
		location = vertexDefaultLocation
	}
	if !vertexLocationPattern.MatchString(location) {
		return "", fmt.Errorf("invalid vertex location: %s", location)
	}
	if model == "" {
		return "", errors.New("vertex model is required")
	}
	action := "rawPredict"
	if stream {
		action = "streamRawPredict"
	}
	host := fmt.Sprintf("%s-aiplatform.googleapis.com", location)
	if location == "global" {
		host = "aiplatform.googleapis.com"
	}
	escapedModel := strings.ReplaceAll(url.PathEscape(model), "%40", "@")
	return fmt.Sprintf(
		"https://%s/v1/projects/%s/locations/%s/publishers/anthropic/models/%s:%s",
		host,
		url.PathEscape(projectID),
		url.PathEscape(location),
		escapedModel,
		action,
	), nil
}

func normalizeVertexAnthropicModelID(model string) string {
	model = strings.TrimSpace(model)
	if model == "" || vertexAnthropicAlreadyDatedIDPattern.MatchString(model) {
		return model
	}
	if m := vertexAnthropicDatedModelIDPattern.FindStringSubmatch(model); len(m) == 3 {
		return m[1] + "@" + m[2]
	}
	return model
}

func buildVertexAnthropicRequestBody(body []byte) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parse anthropic vertex request body: %w", err)
	}
	delete(payload, "model")
	payload["anthropic_version"] = vertexAnthropicVersion
	return json.Marshal(payload)
}

func (a *Account) IsVertexAPIKey() bool {
	return a.IsVertexServiceAccount() && a.Platform == PlatformGemini &&
		strings.TrimSpace(a.GetCredential("auth_mode")) == vertexAuthModeAPIKey
}

type vertexTokenRequestError struct {
	StatusCode  int
	Code        string // 响应的 error 字段，如 invalid_grant
	Description string // 响应的 error_description 字段
	Message     string
}

func (e *vertexTokenRequestError) Error() string {
	return fmt.Sprintf("service account token request returned %d: %s", e.StatusCode, e.Message)
}

// vertexServiceAccountNotFoundError 识别 SA 已不存在：token 端点返回
// 400 {"error":"invalid_grant","error_description":"Invalid grant: account not found"}。
// 该凭据再也换不到 token，账号需要人工更换凭据。
func vertexServiceAccountNotFoundError(err error) (*vertexTokenRequestError, bool) {
	var tokenErr *vertexTokenRequestError
	if !errors.As(err, &tokenErr) {
		return nil, false
	}
	if tokenErr.StatusCode != http.StatusBadRequest || tokenErr.Code != "invalid_grant" ||
		!strings.Contains(strings.ToLower(tokenErr.Description), "account not found") {
		return nil, false
	}
	return tokenErr, true
}
