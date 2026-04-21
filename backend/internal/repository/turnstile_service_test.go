package repository

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"
)

type TurnstileServiceSuite struct {
	suite.Suite
	ctx      context.Context
	verifier *turnstileVerifier
	received chan url.Values
}

func (s *TurnstileServiceSuite) SetupTest() {
	s.ctx = context.Background()
	s.received = make(chan url.Values, 1)
	verifier, ok := NewTurnstileVerifier().(*turnstileVerifier)
	require.True(s.T(), ok, "type assertion failed")
	s.verifier = verifier
}

func (s *TurnstileServiceSuite) setupTransport(handler http.HandlerFunc) {
	s.verifier.verifyURL = "http://in-process/turnstile"
	s.verifier.httpClient = &http.Client{
		Transport: newInProcessTransport(handler, nil),
	}
}

func (s *TurnstileServiceSuite) TestVerifyToken_SendsFormAndDecodesJSON() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Capture form data in main goroutine context later
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		s.received <- values

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(service.TurnstileVerifyResponse{Success: true})
	}))

	resp, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.NoError(s.T(), err, "VerifyToken")
	require.NotNil(s.T(), resp)
	require.True(s.T(), resp.Success, "expected success response")

	// Assert form fields in main goroutine
	select {
	case values := <-s.received:
		require.Equal(s.T(), "sk", values.Get("secret"))
		require.Equal(s.T(), "token", values.Get("response"))
		require.Equal(s.T(), "1.1.1.1", values.Get("remoteip"))
	default:
		require.Fail(s.T(), "expected server to receive request")
	}
}

func (s *TurnstileServiceSuite) TestVerifyToken_ContentType() {
	var contentType string
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentType = r.Header.Get("Content-Type")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(service.TurnstileVerifyResponse{Success: true})
	}))

	_, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.NoError(s.T(), err)
	require.True(s.T(), strings.HasPrefix(contentType, "application/x-www-form-urlencoded"), "unexpected content-type: %s", contentType)
}

func (s *TurnstileServiceSuite) TestVerifyToken_EmptyRemoteIP_NotSent() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		values, _ := url.ParseQuery(string(body))
		s.received <- values

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(service.TurnstileVerifyResponse{Success: true})
	}))

	_, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "")
	require.NoError(s.T(), err)

	select {
	case values := <-s.received:
		require.Equal(s.T(), "", values.Get("remoteip"), "remoteip should be empty or not sent")
	default:
		require.Fail(s.T(), "expected server to receive request")
	}
}

func (s *TurnstileServiceSuite) TestVerifyToken_RequestError() {
	s.verifier.verifyURL = "http://in-process/turnstile"
	s.verifier.httpClient = &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("dial failed")
		}),
	}

	_, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.Error(s.T(), err, "expected error when server is closed")
}

func (s *TurnstileServiceSuite) TestVerifyToken_InvalidJSON() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "not-valid-json")
	}))

	resp, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.Error(s.T(), err, "expected error for invalid JSON response")
	// 契约：JSON 解析失败时仍返回非 nil 的 *response，并把 HTTPStatusCode 填上，
	// 让上层日志能区分「CF edge 返回了非 JSON 的 502 HTML」与「拨号失败」两种故障域。
	require.NotNil(s.T(), resp, "JSON decode failure must still return non-nil response with status populated")
	require.Equal(s.T(), http.StatusOK, resp.HTTPStatusCode)
}

func (s *TurnstileServiceSuite) TestVerifyToken_SuccessFalse() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(service.TurnstileVerifyResponse{
			Success:    false,
			ErrorCodes: []string{"invalid-input-response"},
		})
	}))

	resp, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.NoError(s.T(), err, "VerifyToken should not error on success=false")
	require.NotNil(s.T(), resp)
	require.False(s.T(), resp.Success)
	require.Contains(s.T(), resp.ErrorCodes, "invalid-input-response")
}

func TestTurnstileServiceSuite(t *testing.T) {
	suite.Run(t, new(TurnstileServiceSuite))
}

// TestVerifyToken_PopulatesHTTPStatusAndLatency 回归保护：HTTPStatusCode + LatencyMs
// 必须在 200 路径上被填充（service.VerifyToken 失败日志依赖这两个字段做根因分类）。
func (s *TurnstileServiceSuite) TestVerifyToken_PopulatesHTTPStatusAndLatency() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(service.TurnstileVerifyResponse{
			Success:  true,
			Hostname: "api.tokenkey.dev",
			Action:   "login",
		})
	}))

	resp, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.NoError(s.T(), err)
	require.NotNil(s.T(), resp)
	require.Equal(s.T(), 200, resp.HTTPStatusCode, "200 status must be populated")
	require.GreaterOrEqual(s.T(), resp.LatencyMs, int64(0), "LatencyMs must be set")
	require.Equal(s.T(), "api.tokenkey.dev", resp.Hostname)
	require.Equal(s.T(), "login", resp.Action)
}

// TestVerifyToken_NonOKStatusStillReturnsResponse 回归保护：当 Cloudflare 返回非 2xx
// （例如 429/502），仍要把 HTTPStatusCode 填充给上层日志，便于区分「Cloudflare 拒绝
// token」（200 + success=false）与「Cloudflare 端不可用」（5xx）。
func (s *TurnstileServiceSuite) TestVerifyToken_NonOKStatusStillReturnsResponse() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadGateway)
		_ = json.NewEncoder(w).Encode(service.TurnstileVerifyResponse{Success: false})
	}))

	resp, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.NoError(s.T(), err, "non-2xx with valid JSON body should not return error")
	require.NotNil(s.T(), resp)
	require.Equal(s.T(), http.StatusBadGateway, resp.HTTPStatusCode)
	require.False(s.T(), resp.Success)
}

// TestVerifyToken_BytePreservationOfTrickyChars 回归保护：Cloudflare token 是
// base64 + URL 特殊字符（`+`, `/`, `=`, `.`, `-`, `_`, `:`）的混合体。曾经怀疑过
// `url.Values.Encode()` 改写 token 字节是 invalid-input-response 的根因；该测试把
// 这种可能性永久钉死。
func (s *TurnstileServiceSuite) TestVerifyToken_BytePreservationOfTrickyChars() {
	const trickyToken = "AbC.123_xyz-ABC+def/GHI=jkl:MNO~pqr&STU=vwx?YZ" +
		"01234567890abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ" +
		"+/=" // base64 padding chars sticking around at the end
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		values, err := url.ParseQuery(string(body))
		require.NoError(s.T(), err)
		// 服务器收到的 response 字段必须与原 token 完全一致（包括所有 +/=）
		require.Equal(s.T(), trickyToken, values.Get("response"),
			"token bytes were mutated in transit through url.Values.Encode() — Cloudflare will reject it")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(service.TurnstileVerifyResponse{Success: true})
	}))

	_, err := s.verifier.VerifyToken(s.ctx, "sk", trickyToken, "")
	require.NoError(s.T(), err)
}

// TestVerifyToken_NonJSONResponseStillCarriesStatus 回归保护：CF edge 偶发返回
// HTML 502（非 JSON）时，VerifyToken 必须返回 (非 nil response, err)，且 response
// 带着 HTTPStatusCode + LatencyMs。否则 service 层日志会退化成「无上下文 decode error」，
// 把 2026-04-20 的诊断噩梦拉回来。
func (s *TurnstileServiceSuite) TestVerifyToken_NonJSONResponseStillCarriesStatus() {
	s.setupTransport(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadGateway)
		_, _ = io.WriteString(w, "<html><body>502 Bad Gateway</body></html>")
	}))

	resp, err := s.verifier.VerifyToken(s.ctx, "sk", "token", "1.1.1.1")
	require.Error(s.T(), err, "non-JSON body must surface a decode error")
	require.NotNil(s.T(), resp, "response must be non-nil so caller can log status/latency")
	require.Equal(s.T(), http.StatusBadGateway, resp.HTTPStatusCode)
	require.GreaterOrEqual(s.T(), resp.LatencyMs, int64(0))
	require.False(s.T(), resp.Success, "Success default zero value")
}
