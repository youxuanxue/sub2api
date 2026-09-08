package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// The output-token count feeds the pre-flight hold. Explicit field spellings
// must still be honored as hard reserve inputs, while omitted ceilings use the
// low UX reserve instead of a model-sized maximum that can poison auth balance
// snapshots for several minutes.
func TestTkParseMaxOutputTokens(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"chat max_tokens", `{"max_tokens":1024}`, 1024},
		{"chat max_completion_tokens", `{"max_completion_tokens":2048}`, 2048},
		{"responses max_output_tokens", `{"max_output_tokens":4096}`, 4096},
		{"max of multiple fields", `{"max_tokens":100,"max_output_tokens":300,"max_completion_tokens":200}`, 300},
		{"absent falls back to low reserve", `{}`, tkHoldDefaultOutputReserveTokens},
		{"zero falls back to low reserve", `{"max_tokens":0}`, tkHoldDefaultOutputReserveTokens},
		{"negative falls back to low reserve", `{"max_tokens":-5}`, tkHoldDefaultOutputReserveTokens},
	}
	for _, tc := range cases {
		if got := tkParseMaxOutputTokens([]byte(tc.body)); got != tc.want {
			t.Errorf("%s: tkParseMaxOutputTokens(%s) = %d, want %d", tc.name, tc.body, got, tc.want)
		}
	}
}

func TestTkDefaultOutputReserveTokens_StaysLow(t *testing.T) {
	if tkHoldDefaultOutputReserveTokens != 256 {
		t.Fatalf("default output reserve = %d, want 256", tkHoldDefaultOutputReserveTokens)
	}
	if tkParseMaxOutputTokens([]byte(`{}`)) != tkHoldDefaultOutputReserveTokens {
		t.Fatal("omitted max token fields must use the low default reserve")
	}
}

// The hand-off protocol is the release-ordering half of the overdraft
// invariant: once a usage-record task owns the refund, the handler's deferred
// release must become a no-op — otherwise the hold is refunded while the bill
// is still queued and the funds are re-exposed to concurrent admission.
func TestTkHoldHandle_HandOffDisablesDeferredRelease(t *testing.T) {
	h := &OpenAIGatewayHandler{gatewayService: &service.OpenAIGatewayService{}}
	hh := &tkHoldHandle{h: h, ctx: context.Background(), requestID: "local:req-1"}

	if got := hh.HandOffToSettlement(); got != "local:req-1" {
		t.Fatalf("HandOffToSettlement() = %q, want the hold request id", got)
	}
	if !hh.settling {
		t.Fatal("hand-off must mark the handle as settling")
	}
	// Must be a no-op (and must not panic on the zero gateway service): the
	// settlement transaction owns the refund now.
	hh.ReleaseUnlessSettling()
}

// A nil handle (request not gated: subscription / unpriced / no capability)
// must make both lifecycle calls safe no-ops so call sites need no nil checks.
func TestTkHoldHandle_NilSafe(t *testing.T) {
	var hh *tkHoldHandle
	hh.ReleaseUnlessSettling()
	if got := hh.HandOffToSettlement(); got != "" {
		t.Fatalf("nil handle HandOffToSettlement() = %q, want empty", got)
	}
}

type candidateHoldRepository struct {
	service.UsageBillingRepository
	events     []string
	releaseErr error
	holds      []*service.HoldCommand
	reject     bool
}

func (r *candidateHoldRepository) ReserveBalanceHold(_ context.Context, command *service.HoldCommand) (bool, error) {
	r.events = append(r.events, "reserve")
	r.holds = append(r.holds, command)
	return !r.reject, nil
}

func (r *candidateHoldRepository) ReleaseBalanceHold(context.Context, string) (bool, error) {
	r.events = append(r.events, "release")
	return true, r.releaseErr
}

func (r *candidateHoldRepository) ReleaseExpiredBalanceHolds(context.Context, time.Time, int) (int, error) {
	return 0, nil
}

func (r *candidateHoldRepository) Apply(context.Context, *service.UsageBillingCommand) (*service.UsageBillingApplyResult, error) {
	return &service.UsageBillingApplyResult{}, nil
}

func TestTkHoldRebindReleasesOldReservationBeforeReservingNewOrigin(t *testing.T) {
	repo := &candidateHoldRepository{}
	gateway := service.NewOpenAIGatewayService(nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handle := &tkHoldHandle{h: &OpenAIGatewayHandler{gatewayService: gateway}, ctx: context.Background(), requestID: "request"}
	reserve := func(string) (bool, bool) { repo.events = append(repo.events, "reserve"); return true, false }
	require.NoError(t, handle.rebind("request", false, reserve))
	require.Equal(t, []string{"release", "reserve"}, repo.events)
	require.Equal(t, "request", handle.requestID)

	require.NoError(t, handle.rebind("request", true, reserve))
	require.Equal(t, []string{"release", "reserve", "release"}, repo.events)
	require.Empty(t, handle.requestID, "subscription requests do not retain a balance reservation")

	require.NoError(t, handle.rebind("request", false, reserve))
	require.Equal(t, "request", handle.HandOffToSettlement())
	require.ErrorIs(t, handle.rebind("request", false, reserve), service.ErrUsageBillingRequestConflict)
	handle.ReleaseUnlessSettling()
	require.Equal(t, []string{"release", "reserve", "release", "reserve"}, repo.events)
}

func TestTkHoldDormantSubscriptionCanReserveAfterBalanceFallback(t *testing.T) {
	handle := &tkHoldHandle{}
	calls := 0
	reserve := func(string) (bool, bool) { calls++; return true, false }
	require.NoError(t, handle.rebind("request", true, reserve))
	require.Zero(t, calls)
	require.NoError(t, handle.rebind("request", false, reserve))
	require.Equal(t, 1, calls)
	require.Equal(t, "request", handle.HandOffToSettlement())
}

func TestTkHoldRebindRejectsInsufficientBalanceWithoutAReservation(t *testing.T) {
	handle := &tkHoldHandle{}
	require.ErrorIs(t, handle.rebind("request", false, func(string) (bool, bool) { return false, true }), service.ErrInsufficientBalance)
	require.Empty(t, handle.requestID)
	require.Empty(t, handle.HandOffToSettlement())
}

func TestTkHoldRebindDoesNotReuseReservationAfterFailedRelease(t *testing.T) {
	repo := &candidateHoldRepository{releaseErr: errors.New("database unavailable")}
	gateway := service.NewOpenAIGatewayService(nil, nil, repo, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	handle := &tkHoldHandle{h: &OpenAIGatewayHandler{gatewayService: gateway}, ctx: context.Background(), requestID: "request"}
	err := handle.rebind("request", false, func(string) (bool, bool) { t.Fatal("must not reserve before release succeeds"); return false, false })
	require.ErrorIs(t, err, service.ErrBillingServiceUnavailable)
	require.Equal(t, "request", handle.requestID, "deferred cleanup retains the original hold")
	require.Equal(t, []string{"release"}, repo.events)
}

func TestTkHoldWebSocketNextTurnCannotReuseSettlingReservation(t *testing.T) {
	repo := &candidateHoldRepository{}
	cfg := &config.Config{}
	usage := &openAIWSUsageHandlerUsageLogRepoStub{}
	gateway := service.NewOpenAIGatewayService(nil, usage, repo, nil, nil, nil, nil, cfg, nil, nil,
		service.NewBillingService(cfg, nil), nil, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	h := &OpenAIGatewayHandler{gatewayService: gateway}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/v1/responses", nil)
	c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.RequestID, "candidate-ws"))
	group := &service.Group{ID: 10, RateMultiplier: 1}
	key := &service.APIKey{ID: 7, UserID: 9, User: &service.User{ID: 9}, Group: group, GroupID: &group.ID}
	hold := h.tkNewWSResponsesTurnHold(c, key)
	for turn := 1; turn <= 2; turn++ {
		require.NoError(t, hold.Reserve("gpt-4", []byte(`{"model":"gpt-4","input":"hello"}`)))
		require.True(t, hold.HasHold())
		previous := hold.handle
		h.tkWSAfterTurnSubmitUsage(tkWSAfterTurnUsageInput{
			Ctx: c.Request.Context(), C: c, ReqLog: zap.NewNop(), APIKey: key,
			Account: &service.Account{ID: 63, Platform: service.PlatformOpenAI}, TurnHold: hold,
			TurnPricing: &openAIWSTurnPricing{}, Turn: turn, TurnStart: time.Now(),
			Result: &service.OpenAIForwardResult{RequestID: repo.holds[turn-1].RequestID, Model: "gpt-4", OpenAIWSMode: true,
				UpstreamTerminalEvent: "response.failed"},
			TurnRequestedModel: "gpt-4", TurnUpstreamModel: "gpt-4",
		})
		require.True(t, previous.settling)
		require.False(t, hold.HasHold())
		previous.ReleaseUnlessSettling()
	}
	require.NotEqual(t, repo.holds[0].RequestID, repo.holds[1].RequestID)
	repo.reject = true
	require.Error(t, hold.Reserve("gpt-4", []byte(`{"model":"gpt-4","input":"unfunded turn"}`)))
	require.False(t, hold.HasHold())
	require.Empty(t, hold.HandOffForTurn())
	require.Equal(t, []string{"reserve", "reserve", "reserve"}, repo.events, "settlement owns prior holds even while the next turn is rejected")
}
