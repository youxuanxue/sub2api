package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httpclient"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	turnstileVerifyURL = "https://challenges.cloudflare.com/turnstile/v0/siteverify"
	// 16 KiB 上限：正常 siteverify 响应 <1 KiB；超过此上限说明对端不是 Cloudflare
	// 或返回了异常 payload，截断保护避免 OOM。
	turnstileMaxResponseBytes = 16 * 1024
)

type turnstileVerifier struct {
	httpClient *http.Client
	verifyURL  string
}

func NewTurnstileVerifier() service.TurnstileVerifier {
	sharedClient, err := httpclient.GetClient(httpclient.Options{
		Timeout:            10 * time.Second,
		ValidateResolvedIP: true,
	})
	if err != nil {
		sharedClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &turnstileVerifier{
		httpClient: sharedClient,
		verifyURL:  turnstileVerifyURL,
	}
}

// VerifyToken 调用 Cloudflare siteverify v0 接口。
//
// 返回值约定（service.VerifyToken 的失败日志依赖这套约定，不要随意改动）：
//   - HTTP 网络层失败（拨号超时、TLS 错误等）→ 返回 nil, err。
//   - HTTP 收到响应（无论 2xx / 4xx / 5xx）：
//     **始终返回非 nil 的 \*response**，其中 HTTPStatusCode + LatencyMs 已填充；
//     err 是否为 nil 取决于响应体是否能被 JSON 解析。这样上层日志即便在
//     「CF edge 返回 502 HTML」这种 JSON 解析失败的异常分支也能拿到 status/latency
//     做根因分类，不至于像 2026-04-20 那样退化成「只有一个无上下文的 decode error」。
//
// 这样上层日志能清楚区分「Cloudflare 网络不通」「Cloudflare 限流（429/5xx）」
// 「Cloudflare 拒绝 token（200 + success=false）」「CF edge 返回非 JSON」四种
// 根本不同的故障域。
func (v *turnstileVerifier) VerifyToken(ctx context.Context, secretKey, token, remoteIP string) (*service.TurnstileVerifyResponse, error) {
	formData := url.Values{}
	formData.Set("secret", secretKey)
	formData.Set("response", token)
	if remoteIP != "" {
		formData.Set("remoteip", remoteIP)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, v.verifyURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	start := time.Now()
	resp, err := v.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	latencyMs := time.Since(start).Milliseconds()

	body, err := io.ReadAll(io.LimitReader(resp.Body, turnstileMaxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("read response body (status=%d, latency_ms=%d): %w", resp.StatusCode, latencyMs, err)
	}

	// 关键不变量：先把 HTTP 元数据写入 result，再做 JSON 解析。这样即便解析失败，
	// 调用方拿到的 *response 也带着 status/latency，能做出有意义的诊断。
	result := service.TurnstileVerifyResponse{
		HTTPStatusCode: resp.StatusCode,
		LatencyMs:      latencyMs,
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return &result, fmt.Errorf("decode response (status=%d, body_len=%d): %w", resp.StatusCode, len(body), err)
	}
	// JSON 解析可能覆盖 HTTPStatusCode/LatencyMs（理论上不会，因为它们是 `json:"-"`），
	// 防御性地再赋一次，钉死契约。
	result.HTTPStatusCode = resp.StatusCode
	result.LatencyMs = latencyMs

	return &result, nil
}
