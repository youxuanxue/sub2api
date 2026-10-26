//go:build unit

package service

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	kiroproto "github.com/Wei-Shaw/sub2api/internal/integration/kiro"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// The gateway must measure the payload at the size it will actually send. The
// account profileArn is injected on the dispatch path and carries omitempty, so
// a request validated without it was undercounted by the whole
// `"profileArn":"<arn>"` member. Contract: docs/approved/kiro-payload-overflow.md.
//
// This fixture is prose-only on purpose: the approved policy forbids shortening
// current user prose, so reduction cannot rescue it and the size decision is
// what the test observes.
func TestKiroGatewayService_Forward_RejectsWhenProfileArnPushesOverLimit(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	// A valid terminal response, so a guard that wrongly admits this payload
	// returns success and fails the assertions below fast, instead of blocking
	// the non-streaming path on an unusable upstream body.
	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"admitted","inputTokens":8,"outputTokens":2}`))
	upstream := &kiroFakeUpstream{body: appendKiroTerminalStop(frame, "END_TURN")}
	svc := NewKiroGatewayService(upstream, nil, nil)

	account := newKiroAccountForTest()
	arn := account.GetKiroProfileArn()
	require.NotEmpty(t, arn)

	// Size the prose so the translated payload fits bare but not with the ARN.
	probe := kiroproto.ClaudeToKiro(&kiroproto.ClaudeRequest{
		Model:     "claude-opus-5.5",
		MaxTokens: 16,
		Messages:  []kiroproto.ClaudeMessage{{Role: "user", Content: ""}},
	}, false)
	envelope := len(mustMarshalPayload(t, probe))
	wireCost := len(arn) + len(`,"profileArn":""`)

	// Target a total that sits inside the window the ARN closes.
	prose := kiroPayloadLimitForTest() - envelope - wireCost/2
	request := map[string]any{
		"model":      "claude-opus-5.5",
		"messages":   []map[string]any{{"role": "user", "content": strings.Repeat("x", prose)}},
		"max_tokens": 16,
		"stream":     false,
	}
	body, err := json.Marshal(request)
	require.NoError(t, err)

	// Confirm the fixture really is in that window, or the test proves nothing.
	translated := kiroproto.ClaudeToKiro(&kiroproto.ClaudeRequest{
		Model:     "claude-opus-5.5",
		MaxTokens: 16,
		Messages:  []kiroproto.ClaudeMessage{{Role: "user", Content: strings.Repeat("x", prose)}},
	}, false)
	bare := len(mustMarshalPayload(t, translated))
	require.NoError(t, kiroproto.ValidatePayloadSize(translated),
		"fixture must pass a guard that ignores profileArn")
	require.Greater(t, bare+wireCost, kiroPayloadLimitForTest(),
		"fixture must exceed the limit once the ARN is on the wire")

	result, err := svc.Forward(context.Background(), c, account, &ParsedRequest{
		Body: NewRequestBodyRef(body), Model: "claude-opus-5.5", Stream: false,
	}, time.Now())

	require.Nil(t, result)
	var invalidRequestErr *KiroInvalidRequestError
	require.ErrorAs(t, err, &invalidRequestErr)
	require.Equal(t, http.StatusRequestEntityTooLarge, invalidRequestErr.StatusCode)
	require.False(t, upstream.gotRequest, "the guard must reject before any upstream call")

	// The 413 stays caller-owned: no failover, no account-health penalty.
	var failover *UpstreamFailoverError
	require.False(t, errors.As(err, &failover),
		"a wire-size rejection must not be wrapped as a failover error")
}

// A request with real headroom still reaches upstream: the fix must not cost
// usable capacity beyond the bytes actually sent.
func TestKiroGatewayService_Forward_FittingRequestStillDispatches(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	frame := buildKiroEventStreamMessage("assistantResponseEvent",
		[]byte(`{"content":"ok","inputTokens":8,"outputTokens":2}`))
	upstream := &kiroFakeUpstream{body: appendKiroTerminalStop(frame, "END_TURN")}
	svc := NewKiroGatewayService(upstream, nil, nil)

	body, err := json.Marshal(map[string]any{
		"model":      "claude-opus-5.5",
		"messages":   []map[string]any{{"role": "user", "content": strings.Repeat("x", 4096)}},
		"max_tokens": 16,
		"stream":     false,
	})
	require.NoError(t, err)

	_, err = svc.Forward(context.Background(), c, newKiroAccountForTest(), &ParsedRequest{
		Body: NewRequestBodyRef(body), Model: "claude-opus-5.5", Stream: false,
	}, time.Now())

	require.NoError(t, err)
	require.True(t, upstream.gotRequest, "a fitting request must still be sent")
}

func mustMarshalPayload(t *testing.T, p *kiroproto.KiroPayload) []byte {
	t.Helper()
	raw, err := json.Marshal(p)
	require.NoError(t, err)
	return raw
}

// kiroPayloadLimitForTest recovers the unexported limit from the typed error
// rather than restating 900*1024, so the owner stays single.
func kiroPayloadLimitForTest() int {
	p := &kiroproto.KiroPayload{}
	p.ConversationState.CurrentMessage.UserInputMessage = kiroproto.KiroUserInputMessage{
		Content: strings.Repeat("x", 2*1024*1024),
	}
	var tooLarge *kiroproto.PayloadTooLargeError
	if err := kiroproto.ValidatePayloadSize(p); errors.As(err, &tooLarge) {
		return tooLarge.Limit
	}
	return 0
}
