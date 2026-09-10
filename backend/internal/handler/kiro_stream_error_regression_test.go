//go:build unit

package handler

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type kiroStreamErrorUpstream struct {
	accountIDs []int64
	toolEvent  string
	textFirst  bool
}

func (u *kiroStreamErrorUpstream) Do(r *http.Request, proxy string, id int64, concurrency int) (*http.Response, error) {
	return u.DoWithTLS(r, proxy, id, concurrency, nil)
}

func (u *kiroStreamErrorUpstream) DoWithTLS(_ *http.Request, _ string, id int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	u.accountIDs = append(u.accountIDs, id)
	var frames []byte
	if id == 1001 {
		if u.textFirst {
			frames = gatewayHandlerKiroEventFrame("assistantResponseEvent", []byte(`{"content":"Checking fixture"}`))
		}
		if u.toolEvent == "" {
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(frames))}, nil
		}
		frames = append(frames, gatewayHandlerKiroEventFrame("toolUseEvent", []byte(u.toolEvent))...)
	} else {
		frames = gatewayHandlerKiroEventFrame("assistantResponseEvent", []byte(`{"content":"recovered"}`))
	}
	frames = append(frames, gatewayHandlerKiroEventFrame("metadataEvent", []byte(`{"stopReason":"END_TURN"}`))...)
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(frames))}, nil
}

func kiroStreamErrorAccount(id, groupID int64) *service.Account {
	return &service.Account{
		ID: id, Name: fmt.Sprint(id), Platform: service.PlatformKiro, Type: service.AccountTypeOAuth,
		Credentials: map[string]any{
			"access_token": "fixture", "profile_arn": "arn:aws:codewhisperer:us-east-1:123456789012:profile/test",
			"region": "us-east-1", "auth_method": "social",
		},
		Concurrency: 1, Priority: int(id), Status: service.StatusActive, Schedulable: true,
		AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: groupID}},
	}
}

// Exercise the real bridge and the same error helpers used by ChatCompletions.
// This is not a scheduler/authentication test of the ChatCompletions handler.
func TestKiroChatBridge_CapturedErrorReachesClient(t *testing.T) {
	for _, tc := range []struct{ name, event string }{
		{"malformed", `{"toolUseId":"bad","name":"Bash","input":"{","stop":true}`},
		{"unfinished", `{"toolUseId":"bad","name":"Bash","input":{}}`},
		{"eof", ""},
	} {
		for _, committed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/committed=%t", tc.name, committed), func(t *testing.T) {
				group := &service.Group{ID: 2001, Hydrated: true, Platform: service.PlatformKiro, Status: service.StatusActive}
				account := kiroStreamErrorAccount(1001, group.ID)
				upstream := &kiroStreamErrorUpstream{toolEvent: tc.event, textFirst: true}
				h, cleanup := newGatewayHandlerForKiroRecoveryTest(t, group, []*service.Account{account}, newGatewayHandlerKiroRecoveryCache(), upstream)
				t.Cleanup(cleanup)
				body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"check fixture"}]}`)
				r := gin.New()
				r.POST("/v1/chat/completions", func(c *gin.Context) {
					if committed {
						c.String(http.StatusAccepted, "outer response")
						service.MarkResponseCommitted(c)
					}
					writer := c.Writer
					before := writer.Size()
					result, err := h.gatewayService.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
					require.Error(t, err)
					require.Nil(t, result)
					require.Same(t, writer, c.Writer)
					require.Equal(t, before, c.Writer.Size())
					oh := &OpenAIGatewayHandler{}
					if !openAIForwardErrorAlreadyCommunicated(c, before, err) {
						if !oh.ensureOpenAIStreamReadErrorResponse(c, err, committed) {
							oh.ensureForwardErrorResponseForError(c, err, committed)
						}
					}
					if committed {
						require.True(t, service.IsResponseCommitted(c))
					}
				})
				rec := httptest.NewRecorder()
				r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body)))
				require.Equal(t, []int64{1001}, upstream.accountIDs)
				if committed {
					require.Equal(t, http.StatusAccepted, rec.Code)
					require.Equal(t, "outer response", rec.Body.String())
				} else {
					require.Equal(t, http.StatusBadGateway, rec.Code)
					require.Contains(t, rec.Body.String(), `"type":"upstream_error"`)
					require.NotContains(t, rec.Body.String(), "Checking fixture")
				}
			})
		}
	}
}

func TestGatewayHandlerMessages_InvalidKiroToolFailsOver(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, tc := range []struct{ name, event string }{
			{"malformed", `{"toolUseId":"bad","name":"Bash","input":"{","stop":true}`},
			{"unfinished", `{"toolUseId":"bad","name":"Bash","input":{}}`},
		} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.name, streaming), func(t *testing.T) {
				group := &service.Group{ID: 2001, Hydrated: true, Platform: service.PlatformKiro, Status: service.StatusActive}
				accounts := []*service.Account{kiroStreamErrorAccount(1001, group.ID), kiroStreamErrorAccount(1002, group.ID)}
				upstream := &kiroStreamErrorUpstream{toolEvent: tc.event}
				h, cleanup := newGatewayHandlerForKiroRecoveryTest(t, group, accounts, newGatewayHandlerKiroRecoveryCache(), upstream)
				t.Cleanup(cleanup)
				body := []byte(fmt.Sprintf(`{"model":"claude-opus-4-8","max_tokens":256,"stream":%t,"tools":[{"name":"Bash","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"check fixture"}]}`, streaming))
				c, rec := runGatewayHandlerKiroRecoveryRequest(t, h, group, body)
				require.Equal(t, http.StatusOK, rec.Code)
				require.Contains(t, rec.Body.String(), "recovered")
				require.NotContains(t, rec.Body.String(), `"id":"bad"`)
				require.Equal(t, []int64{1001, 1001, 1002}, upstream.accountIDs)
				events, ok := c.Get(service.OpsUpstreamErrorsKey)
				require.True(t, ok)
				errors := events.([]*service.OpsUpstreamErrorEvent)
				require.NotEmpty(t, errors)
				require.Equal(t, int64(1001), errors[0].AccountID)
				require.Equal(t, "response_error", errors[0].Kind)
				require.Equal(t, "invalid_tool_use", errors[0].Reason)
			})
		}
	}
}

func TestGatewayHandlerMessages_InvalidKiroToolExhaustsPool(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprint(streaming), func(t *testing.T) {
			group := &service.Group{ID: 2001, Hydrated: true, Platform: service.PlatformKiro, Status: service.StatusActive}
			upstream := &kiroStreamErrorUpstream{toolEvent: `{"toolUseId":"bad","name":"Bash","input":"{","stop":true}`}
			h, cleanup := newGatewayHandlerForKiroRecoveryTest(t, group, []*service.Account{kiroStreamErrorAccount(1001, group.ID)}, newGatewayHandlerKiroRecoveryCache(), upstream)
			t.Cleanup(cleanup)
			body := []byte(fmt.Sprintf(`{"model":"claude-opus-4-8","max_tokens":256,"stream":%t,"messages":[{"role":"user","content":"check fixture"}]}`, streaming))
			_, rec := runGatewayHandlerKiroRecoveryRequest(t, h, group, body)
			require.Equal(t, http.StatusBadGateway, rec.Code)
			require.Contains(t, rec.Body.String(), `"type":"upstream_error"`)
			require.NotContains(t, rec.Body.String(), "message_stop")
			require.Equal(t, []int64{1001, 1001}, upstream.accountIDs)
		})
	}
}

func TestKiroChatBridge_ValidStreamReachesClient(t *testing.T) {
	group := &service.Group{ID: 2001, Hydrated: true, Platform: service.PlatformKiro, Status: service.StatusActive}
	account := kiroStreamErrorAccount(1002, group.ID)
	upstream := &kiroStreamErrorUpstream{}
	h, cleanup := newGatewayHandlerForKiroRecoveryTest(t, group, []*service.Account{account}, newGatewayHandlerKiroRecoveryCache(), upstream)
	t.Cleanup(cleanup)
	body := []byte(`{"model":"claude-opus-4-8","stream":true,"messages":[{"role":"user","content":"check fixture"}]}`)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
	result, err := h.gatewayService.ForwardAsChatCompletions(context.Background(), c, account, body, nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.True(t, result.Stream)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), "recovered")
	require.Contains(t, rec.Body.String(), `"finish_reason":"stop"`)
	require.Contains(t, rec.Body.String(), "data: [DONE]")
	require.Equal(t, []int64{1002}, upstream.accountIDs)
}
