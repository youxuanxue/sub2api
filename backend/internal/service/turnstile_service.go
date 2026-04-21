package service

import (
	"context"
	"fmt"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"go.uber.org/zap"
)

var (
	ErrTurnstileVerificationFailed = infraerrors.BadRequest("TURNSTILE_VERIFICATION_FAILED", "turnstile verification failed")
	ErrTurnstileNotConfigured      = infraerrors.ServiceUnavailable("TURNSTILE_NOT_CONFIGURED", "turnstile not configured")
	ErrTurnstileInvalidSecretKey   = infraerrors.BadRequest("TURNSTILE_INVALID_SECRET_KEY", "invalid turnstile secret key")
)

// TurnstileVerifier 验证 Turnstile token 的接口
type TurnstileVerifier interface {
	VerifyToken(ctx context.Context, secretKey, token, remoteIP string) (*TurnstileVerifyResponse, error)
}

// TurnstileService Turnstile 验证服务
type TurnstileService struct {
	settingService *SettingService
	verifier       TurnstileVerifier
}

// TurnstileVerifyResponse Cloudflare Turnstile siteverify v0 响应。
//
// 前 6 个字段对应 Cloudflare 协议字段；后 2 个是 verifier 实现层填充的执行元数据
// （`json:"-"`，不参与 JSON 序列化）。当 Cloudflare 返回 success=false 时，
// HTTPStatusCode / LatencyMs / Hostname / Action 共同决定根因，service.VerifyToken
// 在失败路径上会把它们全部写入结构化日志，避免再像 2026-04-20 那样靠抓 token 反推。
type TurnstileVerifyResponse struct {
	Success     bool     `json:"success"`
	ChallengeTS string   `json:"challenge_ts"`
	Hostname    string   `json:"hostname"`
	ErrorCodes  []string `json:"error-codes"`
	Action      string   `json:"action"`
	CData       string   `json:"cdata"`

	// HTTPStatusCode 是 Cloudflare siteverify 接口返回的 HTTP 状态码（200 = 正常）。
	// 非 200 通常意味着 Cloudflare 端限流或不可用，不应被解读为 token 错误。
	HTTPStatusCode int `json:"-"`
	// LatencyMs 是从发出 HTTP 请求到收到完整响应体的端到端耗时，毫秒。
	LatencyMs int64 `json:"-"`
}

// NewTurnstileService 创建 Turnstile 服务实例
func NewTurnstileService(settingService *SettingService, verifier TurnstileVerifier) *TurnstileService {
	return &TurnstileService{
		settingService: settingService,
		verifier:       verifier,
	}
}

// summarizeToken 把 Turnstile token 摘要成可安全写入日志的字符串。
//
// 设计目标：足够区分「同一个 token 被反复提交」「token 被中间设备截断/重写」
// 等场景，但又绝对不暴露完整 token —— Cloudflare token 是一次性凭证，泄漏即可
// 被滥用一次。安全约束：prefix + suffix 之间至少隐藏 4 个字节，否则把 suffix 抹掉
// 退化为只暴露 prefix。
//
// 阈值：
//   - len < 20 → prefix=min(8,len)，无 suffix（信息量低 + 满足隐藏 4 字节约束）
//   - len ≥ 20 → prefix=10 + suffix=6（中间至少 4 个字节不出现）
//
// 实际 Cloudflare token 长度是 200~300 字节，所以日常都走第二条路径。
func summarizeToken(token string) (length int, prefix, suffix string) {
	length = len(token)
	if length == 0 {
		return 0, "", ""
	}
	if length < 20 {
		end := length
		if end > 8 {
			end = 8
		}
		return length, token[:end], ""
	}
	return length, token[:10], token[length-6:]
}

// VerifyToken 验证 Turnstile token。
//
// 失败路径必须把根因分类所需的全部上下文写入结构化日志：token 摘要、Cloudflare
// 返回的全部 error-codes、Hostname / Action / ChallengeTS、HTTP 状态码与耗时。
// 历史教训：旧版本只 LegacyPrintf 一行 error_codes=[...]，2026-04-20 的诊断
// 因此被迫从远程一遍遍抓 token 反推，浪费数小时。
func (s *TurnstileService) VerifyToken(ctx context.Context, token string, remoteIP string) error {
	if !s.settingService.IsTurnstileEnabled(ctx) {
		logger.With(zap.String("component", "service.turnstile")).
			Debug("[Turnstile] disabled, skipping verification")
		return nil
	}

	secretKey := s.settingService.GetTurnstileSecretKey(ctx)
	if secretKey == "" {
		logger.With(zap.String("component", "service.turnstile")).
			Warn("[Turnstile] secret key not configured")
		return ErrTurnstileNotConfigured
	}

	tokenLen, tokenPrefix, tokenSuffix := summarizeToken(token)
	baseFields := []zap.Field{
		zap.String("component", "service.turnstile"),
		zap.String("remote_ip", remoteIP),
		zap.Int("token_len", tokenLen),
		zap.String("token_prefix", tokenPrefix),
		zap.String("token_suffix", tokenSuffix),
	}

	if token == "" {
		logger.With(baseFields...).Warn("[Turnstile] token is empty (client did not submit cf-turnstile-response)")
		return ErrTurnstileVerificationFailed
	}

	result, err := s.verifier.VerifyToken(ctx, secretKey, token, remoteIP)
	if err != nil {
		// repository 契约：JSON 解析失败时仍会返回非 nil 的 result（带 HTTP status/latency）。
		// 网络层失败（拨号/TLS）才会返回 nil result。两条路径分开记录，方便区分根因。
		errFields := append(baseFields, zap.Error(err))
		if result != nil {
			errFields = append(errFields,
				zap.Int("http_status", result.HTTPStatusCode),
				zap.Int64("latency_ms", result.LatencyMs),
			)
		}
		logger.With(errFields...).Error("[Turnstile] siteverify request failed")
		return fmt.Errorf("siteverify: %w", err)
	}

	resultFields := append(baseFields,
		zap.Int("http_status", result.HTTPStatusCode),
		zap.Int64("latency_ms", result.LatencyMs),
		zap.String("cf_hostname", result.Hostname),
		zap.String("cf_action", result.Action),
		zap.String("cf_cdata", result.CData),
		zap.String("cf_challenge_ts", result.ChallengeTS),
		zap.Strings("cf_error_codes", result.ErrorCodes),
	)

	if !result.Success {
		logger.With(resultFields...).
			Warn("[Turnstile] siteverify returned success=false")
		return ErrTurnstileVerificationFailed
	}

	logger.With(resultFields...).
		Info("[Turnstile] verification successful")
	return nil
}

// IsEnabled 检查 Turnstile 是否启用
func (s *TurnstileService) IsEnabled(ctx context.Context) bool {
	return s.settingService.IsTurnstileEnabled(ctx)
}

// ValidateSecretKey 验证 Turnstile Secret Key 是否有效。
//
// 用一个明显非法的 dummy token 调 siteverify：
//   - 若 Cloudflare 返回 invalid-input-secret → 说明 secret 不属于任何 widget。
//   - 其他错误码（典型是 invalid-input-response）→ secret 有效，token 被拒是预期。
func (s *TurnstileService) ValidateSecretKey(ctx context.Context, secretKey string) error {
	result, err := s.verifier.VerifyToken(ctx, secretKey, "test-validation", "")
	if err != nil {
		return fmt.Errorf("validate secret key: %w", err)
	}

	for _, code := range result.ErrorCodes {
		if code == "invalid-input-secret" {
			return ErrTurnstileInvalidSecretKey
		}
	}

	return nil
}
