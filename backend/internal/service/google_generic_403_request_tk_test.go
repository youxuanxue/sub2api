//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func geminiGenericPermissionDeniedBody() []byte {
	// classifyForbiddenType 的 default。分支不依赖这句文案。
	return []byte(`{"error":{"code":403,"message":"PERMISSION_DENIED","status":"PERMISSION_DENIED"}}`)
}

func TestRateLimitService_HandleUpstreamError_GeminiGeneric403SameRequestTwoAccountsNoSetErrorSecondStopsFailover(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	ctx := withGoogleGeneric403Request(context.Background())
	body := geminiGenericPermissionDeniedBody()

	first := &Account{ID: 1101, Platform: PlatformGemini, Type: AccountTypeAPIKey}
	second := &Account{ID: 1102, Platform: PlatformGemini, Type: AccountTypeAPIKey}

	firstDisable := service.HandleUpstreamError(ctx, first, http.StatusForbidden, http.Header{}, body)
	require.True(t, firstDisable, "同一请求第一个 generic 403 必须允许换号")
	require.False(t, googleGeneric403StopsFailover(ctx))

	repeated := service.HandleUpstreamError(ctx, first, http.StatusForbidden, http.Header{}, body)
	require.True(t, repeated, "同一账号重复 generic 403 仍允许换号，不记成第二次")
	require.False(t, googleGeneric403StopsFailover(ctx))

	secondDisable := service.HandleUpstreamError(ctx, second, http.StatusForbidden, http.Header{}, body)
	require.False(t, secondDisable, "第二个不同账号的 generic 403 必须停止换号")
	require.True(t, googleGeneric403StopsFailover(ctx))

	require.Equal(t, 0, repo.setErrorCalls, "两个账号都不得 SetError")
	require.Equal(t, 0, repo.tempCalls, "generic 403 不得改临时不可调度")
	require.Equal(t, 0, repo.setSchedulableCalls, "generic 403 不得改 schedulable")
	require.Empty(t, repo.lastErrorMsg)

	sameAccountAgain := service.HandleUpstreamError(ctx, first, http.StatusForbidden, http.Header{}, body)
	require.False(t, sameAccountAgain, "已经停止换号后同一请求继续命中也不再 SetError")
	require.Equal(t, 0, repo.setErrorCalls)
}

func TestRateLimitService_HandleUpstreamError_AntigravityGeneric403SameRequestTwoAccountsNoSetErrorSecondStopsFailover(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	ctx := withGoogleGeneric403Request(context.Background())
	body := geminiGenericPermissionDeniedBody()

	firstDisable := service.HandleUpstreamError(ctx, &Account{ID: 1201, Platform: PlatformAntigravity, Type: AccountTypeOAuth}, http.StatusForbidden, http.Header{}, body)
	secondDisable := service.HandleUpstreamError(ctx, &Account{ID: 1202, Platform: PlatformAntigravity, Type: AccountTypeOAuth}, http.StatusForbidden, http.Header{}, body)

	require.True(t, firstDisable)
	require.False(t, secondDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Equal(t, 0, repo.setSchedulableCalls)
}

func TestRateLimitService_HandleUpstreamError_GeminiGeneric403WithoutRequestMarkStillSetError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	account := &Account{ID: 1301, Platform: PlatformGemini, Type: AccountTypeAPIKey}

	shouldDisable := service.HandleUpstreamError(context.Background(), account, http.StatusForbidden, http.Header{}, geminiGenericPermissionDeniedBody())

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls, "没有网关请求标记时 generic 403 仍永久禁用，避免放过单账号 IAM 失败")
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "Access forbidden (403)")
}

func TestRateLimitService_HandleUpstreamError_AntigravityViolation403WithRequestMarkStillSetError(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	ctx := withGoogleGeneric403Request(context.Background())

	shouldDisable := service.HandleUpstreamError(
		ctx,
		&Account{ID: 1401, Platform: PlatformAntigravity, Type: AccountTypeOAuth},
		http.StatusForbidden,
		http.Header{},
		[]byte(`{"error":{"message":"Terms of service violation"}}`),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 1, repo.setErrorCalls)
	require.Equal(t, 0, repo.tempCalls)
	require.Contains(t, repo.lastErrorMsg, "Account violation (403)")
	require.False(t, googleGeneric403StopsFailover(ctx))
}

func TestRateLimitService_HandleUpstreamError_AntigravityValidation403WithRequestMarkStillTemporaryCooldown(t *testing.T) {
	repo := &rateLimitAccountRepoStub{}
	service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
	ctx := withGoogleGeneric403Request(context.Background())

	shouldDisable := service.HandleUpstreamError(
		ctx,
		&Account{ID: 1402, Platform: PlatformAntigravity, Type: AccountTypeOAuth},
		http.StatusForbidden,
		http.Header{},
		antigravityValidationBody(),
	)

	require.True(t, shouldDisable)
	require.Equal(t, 0, repo.setErrorCalls)
	require.Equal(t, 1, repo.tempCalls)
	require.Contains(t, repo.lastTempReason, "Validation required (403)")
	require.False(t, googleGeneric403StopsFailover(ctx))
}

func TestRateLimitService_HandleUpstreamError_GeminiAccountOwned403WithRequestMarkStillSetError(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"error":{"code":403,"message":"Your project has been denied access. Please contact support.","status":"PERMISSION_DENIED"}}`),
		[]byte(`{"error":{"code":403,"message":"Permission denied: Consumer has been suspended.","status":"PERMISSION_DENIED"}}`),
		[]byte(`{"error":{"code":403,"message":"Permission denied on resource project projects/123 for caller pool-sa@proj.iam.gserviceaccount.com","status":"PERMISSION_DENIED"}}`),
		[]byte(`{"error":{"code":403,"message":"insufficient authentication scopes","status":"PERMISSION_DENIED"}}`),
	}
	for _, body := range bodies {
		repo := &rateLimitAccountRepoStub{}
		service := NewRateLimitService(repo, nil, &config.Config{}, nil, nil)
		ctx := withGoogleGeneric403Request(context.Background())

		shouldDisable := service.HandleUpstreamError(
			ctx,
			&Account{ID: 1501, Platform: PlatformGemini, Type: AccountTypeAPIKey},
			http.StatusForbidden,
			http.Header{},
			body,
		)

		require.True(t, shouldDisable)
		require.Equal(t, 1, repo.setErrorCalls, "body=%s", body)
		require.Equal(t, 0, repo.tempCalls)
		require.False(t, googleGeneric403StopsFailover(ctx))
	}
}

func TestHandleUpstreamError_GoogleGeneric403PassthroughKeeps403(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini:generateContent", nil)
	body := geminiGenericPermissionDeniedBody()

	err := (&GeminiMessagesCompatService{}).writeGoogleGeneric403Passthrough(
		c,
		&Account{ID: 1601, Platform: PlatformGemini, Type: AccountTypeAPIKey},
		http.StatusForbidden,
		"req-403",
		body,
	)

	require.Error(t, err)
	require.Equal(t, http.StatusForbidden, recorder.Code)
	require.NotEqual(t, http.StatusBadRequest, recorder.Code)
	require.JSONEq(t, string(body), recorder.Body.String())
}
