//go:build unit

package service

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
	"github.com/stretchr/testify/require"
)

// captureSink 捕获 logger 包通过 sink 通道发出的全部事件，供测试做字段级断言。
type captureSink struct {
	mu     sync.Mutex
	events []*logger.LogEvent
}

func (c *captureSink) WriteLogEvent(event *logger.LogEvent) {
	c.mu.Lock()
	defer c.mu.Unlock()
	dup := *event
	c.events = append(c.events, &dup)
}

func (c *captureSink) snapshot() []*logger.LogEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*logger.LogEvent, len(c.events))
	copy(out, c.events)
	return out
}

// installCaptureSinkOnce 安装全局 capture sink 并在 t.Cleanup 中复位。
//
// 并发安全警告：`logger.SetSink` 是包级 atomic.Value（见 logger.go 中的
// currentSink），多个测试同时持有 sink 会互相串台、cleanup 会抹掉别人的 sink。
//
//   - 调用本 helper 的测试**禁止** `t.Parallel()`。
//   - 同一个 binary 里其它 sink 测试也必须是串行的。
//
// "Once" 仅指 logger.Init 幂等；sink 生命周期是按测试函数线性串起来的。
func installCaptureSinkOnce(t *testing.T) *captureSink {
	t.Helper()
	require.NoError(t, logger.Init(logger.InitOptions{Level: "debug"}))
	sink := &captureSink{}
	logger.SetSink(sink)
	t.Cleanup(func() { logger.SetSink(nil) })
	return sink
}

func newTurnstileServiceForTest(verifier TurnstileVerifier) *TurnstileService {
	cfg := &config.Config{
		Server:    config.ServerConfig{Mode: "release"},
		Turnstile: config.TurnstileConfig{Required: true},
	}
	settings := NewSettingService(&settingRepoStub{values: map[string]string{
		SettingKeyTurnstileEnabled:   "true",
		SettingKeyTurnstileSecretKey: "the-secret",
	}}, cfg)
	return NewTurnstileService(settings, verifier)
}

// realisticCFToken 模拟一个 ~300 字节、形态接近 Cloudflare Turnstile 真实 token
// 的字符串：base64url 字符 + 几个 `.` 段分隔（CF token 在 wire 上长这个样子）。
// 为可重现，用 strings.Repeat 拼成一段确定性的 18 字符 motif × 17 + 尾巴。
var realisticCFToken = strings.Repeat("AbCdEf0123_-./+=Zz", 17) + ".endpad"

// TestSummarizeToken_NeverLeaksFullToken 钉死 token 摘要的安全约束：完整 token
// 永远不能出现在 prefix / suffix 任意一端。
func TestSummarizeToken_NeverLeaksFullToken(t *testing.T) {
	cases := []struct {
		name    string
		token   string
		wantLen int
		wantPre string
		wantSuf string
	}{
		{"empty", "", 0, "", ""},
		{"short_15", "abcdefghijklmno", 15, "abcdefgh", ""},
		{"under_threshold_19", "abcdefghijklmnopqrs", 19, "abcdefgh", ""},
		{"at_threshold_20", "abcdefghijklmnopqrst", 20, "abcdefghij", "opqrst"},
		{
			name:    "realistic_cf_token",
			token:   realisticCFToken,
			wantLen: len(realisticCFToken),
			wantPre: realisticCFToken[:10],
			wantSuf: realisticCFToken[len(realisticCFToken)-6:],
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			length, pre, suf := summarizeToken(tc.token)
			require.Equal(t, tc.wantLen, length)
			require.Equal(t, tc.wantPre, pre)
			require.Equal(t, tc.wantSuf, suf)
			if tc.wantLen >= 20 {
				// 安全约束 1：prefix+suffix 必须比完整 token 短至少 4 字节（中段
				// ≥4 字节被隐藏），不能拼接出完整 token。
				require.LessOrEqual(t, len(pre)+len(suf), tc.wantLen-4,
					"summarizeToken must hide at least 4 bytes of the middle")
				require.NotEqual(t, tc.token, pre+suf,
					"summarizeToken must not concatenate to the full token")
				// 安全约束 2：suffix 不能是 token 中比真实尾段更靠前的子串
				// （即 suffix 必须确实来自 token 末尾，否则函数行为漂移到了
				// 「暴露中段」上）。
				require.True(t, strings.HasSuffix(tc.token, suf),
					"summarizeToken suffix must come from the trailing bytes of token")
				require.True(t, strings.HasPrefix(tc.token, pre),
					"summarizeToken prefix must come from the leading bytes of token")
			}
		})
	}
}

// TestVerifyToken_FailureLogContainsAllDiagnosticFields 钉死失败日志的字段集 ——
// 当 Cloudflare 返回 success=false 时，日志必须包含 token_len / token_prefix /
// token_suffix / remote_ip / http_status / latency_ms / cf_error_codes / cf_hostname /
// cf_action 全部字段。这是 2026-04-20 故障的直接 fix。
func TestVerifyToken_FailureLogContainsAllDiagnosticFields(t *testing.T) {
	sink := installCaptureSinkOnce(t)
	verifier := &turnstileVerifierSpy{
		result: &TurnstileVerifyResponse{
			Success:        false,
			ErrorCodes:     []string{"invalid-input-response"},
			Hostname:       "api.tokenkey.dev",
			Action:         "login",
			ChallengeTS:    "2026-04-20T14:34:06Z",
			HTTPStatusCode: 200,
			LatencyMs:      123,
		},
	}
	svc := newTurnstileServiceForTest(verifier)

	err := svc.VerifyToken(context.Background(), "0.SomeBase64Token+/=Padding-DEADBEEF", "1.2.3.4")
	require.ErrorIs(t, err, ErrTurnstileVerificationFailed)

	events := sink.snapshot()
	require.NotEmpty(t, events, "expected at least one log event")

	var failureEvent *logger.LogEvent
	for _, ev := range events {
		if ev.Level == "warn" && ev.Message == "[Turnstile] siteverify returned success=false" {
			failureEvent = ev
			break
		}
	}
	require.NotNil(t, failureEvent, "expected the success=false warn event")

	require.Equal(t, "service.turnstile", failureEvent.Fields["component"])
	require.Equal(t, "1.2.3.4", failureEvent.Fields["remote_ip"])
	require.EqualValues(t, len("0.SomeBase64Token+/=Padding-DEADBEEF"), failureEvent.Fields["token_len"])
	require.Equal(t, "0.SomeBase", failureEvent.Fields["token_prefix"])
	require.NotEmpty(t, failureEvent.Fields["token_suffix"])
	require.EqualValues(t, 200, failureEvent.Fields["http_status"])
	require.EqualValues(t, 123, failureEvent.Fields["latency_ms"])
	require.Equal(t, "api.tokenkey.dev", failureEvent.Fields["cf_hostname"])
	require.Equal(t, "login", failureEvent.Fields["cf_action"])
	require.Equal(t, "2026-04-20T14:34:06Z", failureEvent.Fields["cf_challenge_ts"])
	// zap MapObjectEncoder encodes zap.Strings as []interface{}（已实测：见
	// review N-2 备注）。如果 zap 升级后行为变了，这里显式 fail 而不是隐藏。
	codes, ok := failureEvent.Fields["cf_error_codes"].([]interface{})
	require.True(t, ok, "cf_error_codes type changed: got %T (zap upgrade?)", failureEvent.Fields["cf_error_codes"])
	require.Contains(t, codes, "invalid-input-response")
}

// TestVerifyToken_EmptyTokenLogsExplicitly 空 token 路径必须输出明确的诊断日志，
// 而不是悄悄返回 ErrTurnstileVerificationFailed。
func TestVerifyToken_EmptyTokenLogsExplicitly(t *testing.T) {
	sink := installCaptureSinkOnce(t)
	verifier := &turnstileVerifierSpy{}
	svc := newTurnstileServiceForTest(verifier)

	err := svc.VerifyToken(context.Background(), "", "1.2.3.4")
	require.ErrorIs(t, err, ErrTurnstileVerificationFailed)
	require.Equal(t, 0, verifier.called, "verifier must not be called when token is empty")

	events := sink.snapshot()
	var found *logger.LogEvent
	for _, ev := range events {
		if ev.Message == "[Turnstile] token is empty (client did not submit cf-turnstile-response)" {
			found = ev
			break
		}
	}
	require.NotNil(t, found, "expected explicit empty-token log event")
	require.Equal(t, "1.2.3.4", found.Fields["remote_ip"])
	require.EqualValues(t, 0, found.Fields["token_len"])
}
