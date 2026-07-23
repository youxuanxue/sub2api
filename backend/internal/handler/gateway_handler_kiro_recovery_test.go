//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type gatewayHandlerKiroRecoveryCache struct {
	service.GatewayCache
	sticky      map[string]int64
	recovery    map[string]int64
	consumeCall int
}

func newGatewayHandlerKiroRecoveryCache() *gatewayHandlerKiroRecoveryCache {
	return &gatewayHandlerKiroRecoveryCache{
		sticky:   make(map[string]int64),
		recovery: make(map[string]int64),
	}
}

func gatewayHandlerKiroRecoveryKey(groupID int64, sessionHash string) string {
	return fmt.Sprintf("%d:%s", groupID, sessionHash)
}

func (c *gatewayHandlerKiroRecoveryCache) GetSessionAccountID(_ context.Context, groupID int64, sessionHash string) (int64, error) {
	return c.sticky[gatewayHandlerKiroRecoveryKey(groupID, sessionHash)], nil
}

func (c *gatewayHandlerKiroRecoveryCache) SetSessionAccountID(_ context.Context, groupID int64, sessionHash string, accountID int64, _ time.Duration) error {
	c.sticky[gatewayHandlerKiroRecoveryKey(groupID, sessionHash)] = accountID
	return nil
}

func (c *gatewayHandlerKiroRecoveryCache) RefreshSessionTTL(context.Context, int64, string, time.Duration) error {
	return nil
}

func (c *gatewayHandlerKiroRecoveryCache) DeleteSessionAccountID(_ context.Context, groupID int64, sessionHash string) error {
	delete(c.sticky, gatewayHandlerKiroRecoveryKey(groupID, sessionHash))
	return nil
}

func (c *gatewayHandlerKiroRecoveryCache) SetKiroSessionRecoveryExclusion(_ context.Context, groupID int64, sessionHash string, accountID int64, _ time.Duration) error {
	key := gatewayHandlerKiroRecoveryKey(groupID, sessionHash)
	c.recovery[key] = accountID
	delete(c.sticky, key)
	return nil
}

func (c *gatewayHandlerKiroRecoveryCache) ConsumeKiroSessionRecoveryExclusion(_ context.Context, groupID int64, sessionHash string) (int64, error) {
	c.consumeCall++
	key := gatewayHandlerKiroRecoveryKey(groupID, sessionHash)
	accountID := c.recovery[key]
	delete(c.recovery, key)
	return accountID, nil
}

type gatewayHandlerKiroRecoveryUpstream struct {
	accountIDs []int64
}

func (u *gatewayHandlerKiroRecoveryUpstream) Do(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, accountConcurrency, nil)
}

func (u *gatewayHandlerKiroRecoveryUpstream) DoWithTLS(_ *http.Request, _ string, accountID int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.accountIDs = append(u.accountIDs, accountID)
	partial := gatewayHandlerKiroEventFrame("assistantResponseEvent", []byte(`{"content":"partial answer"}`))
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(partial)),
	}, nil
}

func gatewayHandlerKiroEventFrame(eventType string, payload []byte) []byte {
	const headerName = ":event-type"
	var headers bytes.Buffer
	_ = headers.WriteByte(byte(len(headerName)))
	_, _ = headers.WriteString(headerName)
	_ = headers.WriteByte(7)
	var valueLen [2]byte
	binary.BigEndian.PutUint16(valueLen[:], uint16(len(eventType)))
	_, _ = headers.Write(valueLen[:])
	_, _ = headers.WriteString(eventType)

	var frame bytes.Buffer
	var u32 [4]byte
	totalLen := 12 + headers.Len() + len(payload) + 4
	binary.BigEndian.PutUint32(u32[:], uint32(totalLen))
	_, _ = frame.Write(u32[:])
	binary.BigEndian.PutUint32(u32[:], uint32(headers.Len()))
	_, _ = frame.Write(u32[:])
	_, _ = frame.Write([]byte{0, 0, 0, 0})
	_, _ = frame.Write(headers.Bytes())
	_, _ = frame.Write(payload)
	_, _ = frame.Write([]byte{0, 0, 0, 0})
	return frame.Bytes()
}

func newGatewayHandlerForKiroRecoveryTest(
	t *testing.T,
	group *service.Group,
	accounts []*service.Account,
	cache service.GatewayCache,
	upstream service.HTTPUpstream,
) (*GatewayHandler, func()) {
	t.Helper()
	schedulerCache := &fakeSchedulerCache{accounts: accounts}
	schedulerSnapshot := service.NewSchedulerSnapshotService(schedulerCache, nil, nil, nil, nil)
	kiroGateway := service.NewKiroGatewayService(upstream, nil, nil)
	gwSvc := service.NewGatewayService(
		nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, cache, nil,
		schedulerSnapshot, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, nil, nil, nil, kiroGateway,
	)

	cfg := &config.Config{RunMode: config.RunModeSimple}
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	concurrencySvc := service.NewConcurrencyService(&fakeConcurrencyCache{})
	h := &GatewayHandler{
		gatewayService:           gwSvc,
		billingCacheService:      billingCacheSvc,
		concurrencyHelper:        NewConcurrencyHelper(concurrencySvc, SSEPingFormatClaude, 0),
		cfg:                      cfg,
		maxAccountSwitches:       1,
		maxAccountSwitchesGemini: 1,
	}
	return h, billingCacheSvc.Stop
}

func runGatewayHandlerKiroRecoveryRequest(t *testing.T, h *GatewayHandler, group *service.Group, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
	c.Request = req
	apiKey := &service.APIKey{
		ID:      3001,
		UserID:  4001,
		GroupID: &group.ID,
		Status:  service.StatusActive,
		User: &service.User{
			ID:          4001,
			Concurrency: 10,
			Balance:     100,
		},
		Group: group,
	}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
	h.Messages(c)
	return c, rec
}

func TestGatewayHandlerMessages_KiroDisconnectMakesContinuationSelectAnotherAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	const metadataUserID = "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_" + sessionID
	group := &service.Group{ID: 2001, Hydrated: true, Platform: service.PlatformKiro, Status: service.StatusActive}
	failed := &service.Account{
		ID: 1001, Name: "kiro-failed", Platform: service.PlatformKiro, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "at", "refresh_token": "rt",
			"profile_arn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/failed",
			"region":      "us-east-1", "auth_method": "social",
		},
		Concurrency: 1, Priority: 1, Status: service.StatusActive, Schedulable: true,
		AccountGroups: []service.AccountGroup{{AccountID: 1001, GroupID: group.ID}},
	}
	replacement := &service.Account{
		ID: 1002, Name: "kiro-replacement", Platform: service.PlatformKiro, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "at", "intercept_warmup_requests": true},
		Concurrency: 1, Priority: 1, Status: service.StatusActive, Schedulable: true,
		AccountGroups: []service.AccountGroup{{AccountID: 1002, GroupID: group.ID}},
	}
	cache := newGatewayHandlerKiroRecoveryCache()
	cache.sticky[gatewayHandlerKiroRecoveryKey(group.ID, sessionID)] = failed.ID
	upstream := &gatewayHandlerKiroRecoveryUpstream{}
	h, cleanup := newGatewayHandlerForKiroRecoveryTest(t, group, []*service.Account{failed, replacement}, cache, upstream)
	t.Cleanup(cleanup)

	firstBody := []byte(`{"model":"claude-opus-4-8","max_tokens":256,"stream":true,"metadata":{"user_id":"` + metadataUserID + `"},"messages":[{"role":"user","content":"do the long task"}]}`)
	firstCtx, firstRec := runGatewayHandlerKiroRecoveryRequest(t, h, group, firstBody)
	selected, ok := firstCtx.Get(opsAccountIDKey)
	require.True(t, ok)
	require.Equal(t, failed.ID, selected)
	require.Contains(t, firstRec.Body.String(), "partial answer")
	require.Contains(t, firstRec.Body.String(), "event: error")
	require.Equal(t, failed.ID, cache.recovery[gatewayHandlerKiroRecoveryKey(group.ID, sessionID)])
	require.Zero(t, cache.sticky[gatewayHandlerKiroRecoveryKey(group.ID, sessionID)])
	require.Equal(t, []int64{failed.ID}, upstream.accountIDs)
	require.Equal(t, 1, cache.consumeCall, "the interrupted request finds no pre-existing recovery marker")

	continueBody := []byte(`{"model":"claude-opus-4-8","max_tokens":256,"metadata":{"user_id":"` + metadataUserID + `"},"messages":[{"role":"user","content":[{"type":"text","text":"Warmup"}]}]}`)
	continueCtx, continueRec := runGatewayHandlerKiroRecoveryRequest(t, h, group, continueBody)
	selected, ok = continueCtx.Get(opsAccountIDKey)
	require.True(t, ok)
	require.Equal(t, replacement.ID, selected)
	require.Equal(t, http.StatusOK, continueRec.Code)
	require.Contains(t, continueRec.Body.String(), "msg_mock_warmup")
	require.Equal(t, 2, cache.consumeCall, "the continuation atomically consumes the newly recorded marker")
	require.Zero(t, cache.recovery[gatewayHandlerKiroRecoveryKey(group.ID, sessionID)])
	require.Equal(t, []int64{failed.ID}, upstream.accountIDs, "continuation must not call the failed account")
}

func TestGatewayHandlerMessages_NonKiroRequestDoesNotConsumeKiroRecovery(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const sessionID = "123e4567-e89b-12d3-a456-426614174000"
	const metadataUserID = "user_a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2_account__session_" + sessionID
	group := &service.Group{ID: 2002, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
	account := &service.Account{
		ID: 1003, Name: "ag-warmup", Platform: service.PlatformAntigravity, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{"access_token": "at", "intercept_warmup_requests": true},
		Extra:       map[string]any{"mixed_scheduling": true},
		Concurrency: 1, Priority: 1, Status: service.StatusActive, Schedulable: true,
		AccountGroups: []service.AccountGroup{{AccountID: 1003, GroupID: group.ID}},
	}
	cache := newGatewayHandlerKiroRecoveryCache()
	key := gatewayHandlerKiroRecoveryKey(group.ID, sessionID)
	cache.recovery[key] = 999
	h, cleanup := newGatewayHandlerForKiroRecoveryTest(t, group, []*service.Account{account}, cache, &gatewayHandlerKiroRecoveryUpstream{})
	t.Cleanup(cleanup)

	body := []byte(`{"model":"claude-sonnet-4-6","max_tokens":256,"metadata":{"user_id":"` + metadataUserID + `"},"messages":[{"role":"user","content":[{"type":"text","text":"Warmup"}]}]}`)
	_, rec := runGatewayHandlerKiroRecoveryRequest(t, h, group, body)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, 0, cache.consumeCall)
	require.Equal(t, int64(999), cache.recovery[key])
}

var _ service.KiroSessionRecoveryStore = (*gatewayHandlerKiroRecoveryCache)(nil)
