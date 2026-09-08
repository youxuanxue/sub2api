package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

type candidateWSSocketState struct {
	mu           sync.Mutex
	key          service.APIKey
	user         service.User
	groups       []service.Group
	subscription *service.UserSubscription
	keyReadErr   error
	accountBusy  bool
	accountLimit int
	userBusy     bool
}

type candidateWSKeyRepo struct {
	service.APIKeyRepository
	state *candidateWSSocketState
}

func (r candidateWSKeyRepo) GetByID(_ context.Context, _ int64) (*service.APIKey, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.keyReadErr != nil {
		return nil, r.state.keyReadErr
	}
	key, user := r.state.key, r.state.user
	user.AllowedGroups = append([]int64(nil), user.AllowedGroups...)
	key.User = &user
	return &key, nil
}

type candidateWSUserRepo struct {
	service.UserRepository
	state *candidateWSSocketState
}

func (r candidateWSUserRepo) GetByID(_ context.Context, _ int64) (*service.User, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	user := r.state.user
	user.AllowedGroups = append([]int64(nil), user.AllowedGroups...)
	return &user, nil
}
func (r candidateWSUserRepo) DeductBalance(_ context.Context, _ int64, amount float64) error {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	r.state.user.Balance -= amount
	return nil
}

type candidateWSGroupRepo struct {
	service.GroupRepository
	state *candidateWSSocketState
}

func (r candidateWSGroupRepo) ListActive(_ context.Context) ([]service.Group, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	return append([]service.Group(nil), r.state.groups...), nil
}
func (r candidateWSGroupRepo) GetByID(ctx context.Context, id int64) (*service.Group, error) {
	groups, err := r.ListActive(ctx)
	for _, group := range groups {
		if group.ID == id {
			return &group, err
		}
	}
	return nil, errors.New("group unavailable")
}

type candidateWSSubscriptionRepo struct {
	service.UserSubscriptionRepository
	state *candidateWSSocketState
}

func (r candidateWSSubscriptionRepo) ListActiveByUserID(_ context.Context, _ int64) ([]service.UserSubscription, error) {
	r.state.mu.Lock()
	defer r.state.mu.Unlock()
	if r.state.subscription == nil || !r.state.subscription.IsActive() {
		return nil, nil
	}
	return []service.UserSubscription{*r.state.subscription}, nil
}
func (r candidateWSSubscriptionRepo) GetActiveByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	subs, err := r.ListActiveByUserID(ctx, userID)
	for _, sub := range subs {
		if sub.GroupID == groupID {
			return &sub, err
		}
	}
	return nil, service.ErrSubscriptionNotFound
}
func (r candidateWSSubscriptionRepo) GetByUserIDAndGroupID(ctx context.Context, userID, groupID int64) (*service.UserSubscription, error) {
	return r.GetActiveByUserIDAndGroupID(ctx, userID, groupID)
}
func (r candidateWSSubscriptionRepo) IncrementUsage(_ context.Context, _ int64, _ float64) error {
	return nil
}

type candidateWSAccountRepo struct {
	openAIWSUsageHandlerAccountRepoStub
	mu sync.Mutex
}

func (r *candidateWSAccountRepo) GetByID(_ context.Context, id int64) (*service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.account.ID != id {
		return nil, service.ErrAccountNotFound
	}
	account := r.account
	return &account, nil
}

func (r *candidateWSAccountRepo) ListCandidateAccounts(_ context.Context, groups []int64) ([]service.Account, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, group := range groups {
		for _, attached := range r.account.GroupIDs {
			if group == attached {
				return []service.Account{r.account}, nil
			}
		}
	}
	return nil, nil
}

type candidateWSSocketFixture struct {
	state    *candidateWSSocketState
	url      string
	upstream chan []byte
	usage    chan *service.UsageLog
	service  *service.OpenAIGatewayService
	accounts *candidateWSAccountRepo
}

func newCandidateWSSocketFixture(t *testing.T, mode string, subscribed bool) *candidateWSSocketFixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	upstream := make(chan []byte, 8)
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for turn := 1; ; turn++ {
			_, payload, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			upstream <- append([]byte(nil), payload...)
			response := fmt.Sprintf(`{"type":"response.completed","response":{"id":"resp_candidate_%d","model":%q,"usage":{"input_tokens":2,"output_tokens":1}}}`, turn, gjson.GetBytes(payload, "model").String())
			if err := conn.Write(r.Context(), coderws.MessageText, []byte(response)); err != nil {
				return
			}
		}
	}))
	t.Cleanup(upstreamServer.Close)
	state := &candidateWSSocketState{
		key:    service.APIKey{ID: 20, UserID: 10, Status: service.StatusActive, RoutingMode: service.RoutingModeUniversal},
		user:   service.User{ID: 10, Status: service.StatusActive, Balance: 100, Concurrency: 2, AllowedGroups: []int64{1, 11}},
		groups: []service.Group{{ID: 1, Platform: service.PlatformAnthropic, Status: service.StatusActive, RateMultiplier: 1, IsExclusive: true}},
	}
	if subscribed {
		state.user.Balance = 0
		state.groups = append(state.groups, service.Group{ID: 11, Platform: service.PlatformGemini, Status: service.StatusActive, RateMultiplier: 1, SubscriptionType: "subscription"})
		now := time.Now()
		state.subscription = &service.UserSubscription{ID: 30, UserID: 10, GroupID: 11, Status: service.SubscriptionStatusActive, StartsAt: now, ExpiresAt: now.Add(time.Hour), DailyWindowStart: &now, WeeklyWindowStart: &now, MonthlyWindowStart: &now}
	}
	account := service.Account{ID: 115, Name: "candidate-ws", Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Status: service.StatusActive, Schedulable: true, Concurrency: 2, GroupIDs: []int64{1, 11},
		Credentials: map[string]any{"api_key": "test-credential", "base_url": upstreamServer.URL, "model_mapping": map[string]any{"gpt-5.4": "gpt-5.4", "gpt-4.1": "gpt-4.1"}},
		Extra:       map[string]any{"openai_apikey_responses_websockets_v2_enabled": true, "openai_apikey_responses_websockets_v2_mode": mode},
	}
	identity, governed, err := service.BuildProtocolEndpointIdentity(&account)
	require.NoError(t, err)
	require.True(t, governed)
	capabilityID := int64(100115)
	account.ProtocolEndpointCapabilityID = &capabilityID
	account.ProtocolEndpointCapability = &service.ProtocolEndpointCapability{ID: capabilityID, Identity: identity, CapabilityKey: identity.Key(), SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolResponses}, Revision: 1, ProbeEvidence: service.ProtocolProbeEvidence{InitialProbeCompleted: true}}
	cfg := &config.Config{}
	cfg.RunMode = config.RunModeSimple
	if subscribed {
		cfg.RunMode = config.RunModeStandard
	}
	cfg.Default.RateMultiplier = 1
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.Enabled, cfg.Gateway.OpenAIWS.APIKeyEnabled, cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2, cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true, true, true, true
	cfg.Gateway.OpenAIWS.DialTimeoutSeconds, cfg.Gateway.OpenAIWS.ReadTimeoutSeconds, cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3, 3, 3
	keyRepo, userRepo, groupRepo, subRepo := candidateWSKeyRepo{state: state}, candidateWSUserRepo{state: state}, candidateWSGroupRepo{state: state}, candidateWSSubscriptionRepo{state: state}
	accountRepo := &candidateWSAccountRepo{openAIWSUsageHandlerAccountRepoStub: openAIWSUsageHandlerAccountRepoStub{account: account}}
	usage := &openAIWSUsageHandlerUsageLogRepoStub{created: make(chan *service.UsageLog, 8)}
	billingCache := service.NewBillingCacheService(nil, userRepo, subRepo, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCache.Stop)
	keyService := service.NewAPIKeyService(keyRepo, userRepo, groupRepo, subRepo, nil, nil, cfg)
	concurrency := service.NewConcurrencyService(&concurrencyCacheMock{
		acquireUserSlotFn: func(context.Context, int64, int, string) (bool, error) {
			state.mu.Lock()
			defer state.mu.Unlock()
			return !state.userBusy, nil
		},
		acquireAccountSlotFn: func(_ context.Context, _ int64, maximum int, _ string) (bool, error) {
			state.mu.Lock()
			defer state.mu.Unlock()
			state.accountLimit = maximum
			return !state.accountBusy, nil
		},
	})
	billing := service.NewBillingService(cfg, nil)
	openai := service.NewOpenAIGatewayService(accountRepo, usage, nil, userRepo, subRepo, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil)
	gateway := service.NewGatewayService(accountRepo, groupRepo, usage, nil, userRepo, subRepo, nil, nil, cfg, nil, concurrency, billing, nil, billingCache, nil, nil, &service.DeferredService{}, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)
	subs := service.NewSubscriptionService(groupRepo, subRepo, billingCache, nil, cfg)
	t.Cleanup(subs.Stop)
	protocolRouter := service.NewProtocolRouter()
	service.ProvideTKUniversalModelsProvider(keyService, gateway, subs, openai, protocolRouter)
	h := &OpenAIGatewayHandler{gatewayService: openai, billingCacheService: billingCache, apiKeyService: keyService, concurrencyHelper: NewConcurrencyHelper(concurrency, SSEPingFormatNone, time.Second), cfg: cfg, protocolRouter: protocolRouter}
	router := gin.New()
	router.Use(func(c *gin.Context) {
		key, err := keyRepo.GetByID(c.Request.Context(), 20)
		if err != nil {
			c.AbortWithStatus(401)
			return
		}
		c.Set(string(middleware.ContextKeyAPIKey), key)
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 10, Concurrency: 2})
		c.Next()
	})
	router.GET("/v1/responses", h.ResponsesWebSocket)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return &candidateWSSocketFixture{state: state, url: "ws" + strings.TrimPrefix(server.URL, "http") + "/v1/responses", upstream: upstream, usage: usage.created, service: openai, accounts: accountRepo}
}

func (f *candidateWSSocketFixture) dial(t *testing.T) *coderws.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, _, err := coderws.Dial(ctx, f.url, nil)
	require.NoError(t, err)
	t.Cleanup(func() { _ = conn.CloseNow() })
	return conn
}

func candidateWSWrite(t *testing.T, conn *coderws.Conn, payload string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, conn.Write(ctx, coderws.MessageText, []byte(payload)))
}
func candidateWSRead(t *testing.T, conn *coderws.Conn) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := conn.Read(ctx)
	return data, err
}

func TestCandidateWebSocket_FirstFrameAndEachTurnUseAuthorizedPlan(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		t.Run(mode, func(t *testing.T) {
			fixture := newCandidateWSSocketFixture(t, mode, false)
			conn := fixture.dial(t)
			for _, model := range []string{"gpt-5.4", "gpt-4.1"} {
				candidateWSWrite(t, conn, fmt.Sprintf(`{"type":"response.create","model":%q,"input":"hello"}`, model))
				data, err := candidateWSRead(t, conn)
				require.NoError(t, err)
				require.Equal(t, "response.completed", gjson.GetBytes(data, "type").String())
				select {
				case payload := <-fixture.upstream:
					require.Equal(t, model, gjson.GetBytes(payload, "model").String())
				case <-time.After(time.Second):
					t.Fatal("upstream frame missing")
				}
			}
		})
	}
}

func TestCandidateWebSocket_ReasoningPolicyFollowsEachTurnBillingOrigin(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		for _, policy := range []string{"maximum", "mapping"} {
			for _, efforts := range [][2]string{{"low", "high"}, {"high", "low"}} {
				t.Run(mode+"/"+policy+"/"+efforts[0]+"_to_"+efforts[1], func(t *testing.T) {
					fixture := newCandidateWSSocketFixture(t, mode, false)
					setOrigin := func(groupID int64, effort string) {
						group := service.Group{ID: groupID, Platform: service.PlatformOpenAI, Status: service.StatusActive, RateMultiplier: 1, IsExclusive: true}
						if policy == "maximum" {
							group.MaxReasoningEffort = effort
						} else {
							group.ReasoningEffortMappings = []service.ReasoningEffortMapping{{From: "high", To: effort}}
						}
						fixture.state.mu.Lock()
						fixture.state.groups = []service.Group{group}
						fixture.state.mu.Unlock()
					}
					setOrigin(1, efforts[0])
					conn := fixture.dial(t)
					for turn, effort := range efforts {
						if turn > 0 {
							setOrigin(11, effort)
						}
						candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"hello","reasoning":{"effort":"high"}}`)
						_, err := candidateWSRead(t, conn)
						require.NoError(t, err)
						select {
						case payload := <-fixture.upstream:
							require.Equal(t, effort, gjson.GetBytes(payload, "reasoning.effort").String(), "turn %d must use its current billing origin's policy", turn+1)
						case <-time.After(time.Second):
							t.Fatal("upstream frame missing")
						}
					}
				})
			}
		}
	}
}

func TestCandidateWebSocket_RechecksKeyAndEntitlementBeforeNextFrame(t *testing.T) {
	cases := map[string]func(*candidateWSSocketState){
		"disabled":              func(s *candidateWSSocketState) { s.key.Status = "disabled" },
		"expired":               func(s *candidateWSSocketState) { past := time.Now().Add(-time.Minute); s.key.ExpiresAt = &past },
		"quota":                 func(s *candidateWSSocketState) { s.key.Quota = 1; s.key.QuotaUsed = 1 },
		"authorization_removed": func(s *candidateWSSocketState) { s.user.AllowedGroups = nil },
		"user_disabled":         func(s *candidateWSSocketState) { s.user.Status = "disabled" },
		"query_failed":          func(s *candidateWSSocketState) { s.keyReadErr = errors.New("database unavailable") },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			fixture := newCandidateWSSocketFixture(t, service.OpenAIWSIngressModePassthrough, false)
			conn := fixture.dial(t)
			candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"hello"}`)
			_, err := candidateWSRead(t, conn)
			require.NoError(t, err)
			<-fixture.upstream
			fixture.state.mu.Lock()
			change(fixture.state)
			fixture.state.mu.Unlock()
			candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"again"}`)
			_, err = candidateWSRead(t, conn)
			require.Error(t, err)
			require.NotEqual(t, coderws.StatusCode(-1), coderws.CloseStatus(err))
			select {
			case payload := <-fixture.upstream:
				t.Fatalf("unauthorized frame reached upstream: %s", payload)
			default:
			}
		})
	}
}

func TestCandidateWebSocket_ChangedExecutionRequiresReconnect(t *testing.T) {
	for _, mode := range []string{service.OpenAIWSIngressModePassthrough, service.OpenAIWSIngressModeCtxPool} {
		for _, changed := range []string{"model_mapping", "api_key"} {
			t.Run(mode+"/"+changed, func(t *testing.T) {
				fixture := newCandidateWSSocketFixture(t, mode, false)
				conn := fixture.dial(t)
				candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"hello"}`)
				_, err := candidateWSRead(t, conn)
				require.NoError(t, err)
				<-fixture.upstream
				fixture.accounts.mu.Lock()
				credentials := make(map[string]any, len(fixture.accounts.account.Credentials))
				for key, value := range fixture.accounts.account.Credentials {
					credentials[key] = value
				}
				if changed == "model_mapping" {
					credentials[changed] = map[string]any{"gpt-5.4": "gpt-4.1", "gpt-4.1": "gpt-4.1"}
				} else {
					credentials[changed] = "rotated-credential"
				}
				fixture.accounts.account.Credentials = credentials
				fixture.accounts.mu.Unlock()
				candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"again"}`)
				_, err = candidateWSRead(t, conn)
				require.Error(t, err)
				require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err))
				select {
				case payload := <-fixture.upstream:
					t.Fatalf("frame reached stale upstream execution: %s", payload)
				default:
				}
			})
		}
	}
}

func TestCandidateWebSocket_NextTurnUsesCurrentAccountConcurrency(t *testing.T) {
	fixture := newCandidateWSSocketFixture(t, service.OpenAIWSIngressModePassthrough, false)
	conn := fixture.dial(t)
	candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"hello"}`)
	_, err := candidateWSRead(t, conn)
	require.NoError(t, err)
	<-fixture.upstream
	fixture.accounts.mu.Lock()
	fixture.accounts.account.Concurrency = 1
	fixture.accounts.mu.Unlock()
	candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"again"}`)
	_, err = candidateWSRead(t, conn)
	require.NoError(t, err)
	fixture.state.mu.Lock()
	maximum := fixture.state.accountLimit
	fixture.state.mu.Unlock()
	require.Equal(t, 1, maximum)
}

func TestCandidateWebSocket_ZeroWalletUsesActiveSubscription(t *testing.T) {
	fixture := newCandidateWSSocketFixture(t, service.OpenAIWSIngressModePassthrough, true)
	conn := fixture.dial(t)
	candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"hello"}`)
	_, err := candidateWSRead(t, conn)
	require.NoError(t, err)
	select {
	case usage := <-fixture.usage:
		require.NotNil(t, usage.SubscriptionID)
		require.Equal(t, int64(30), *usage.SubscriptionID)
	case <-time.After(3 * time.Second):
		t.Fatal("subscription usage missing")
	}
}

func TestCandidateWebSocket_RejectsInvalidFirstFrameBeforeUpstream(t *testing.T) {
	for _, payload := range []string{`{`, `{"type":"response.create","input":"hello"}`, `{"type":"session.update","model":"gpt-5.4"}`, `{"type":"response.create","model":"gpt-5.4","previous_response_id":"resp_unknown","input":"hello"}`} {
		t.Run(payload, func(t *testing.T) {
			fixture := newCandidateWSSocketFixture(t, service.OpenAIWSIngressModePassthrough, false)
			conn := fixture.dial(t)
			candidateWSWrite(t, conn, payload)
			_, err := candidateWSRead(t, conn)
			require.Error(t, err)
			select {
			case frame := <-fixture.upstream:
				t.Fatalf("invalid frame reached upstream: %s", frame)
			default:
			}
		})
	}
}

func TestCandidateWebSocket_ContinuationPreservesOwnerAndAccountAcrossKeyChange(t *testing.T) {
	fixture := newCandidateWSSocketFixture(t, service.OpenAIWSIngressModePassthrough, false)
	conn := fixture.dial(t)
	candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"hello"}`)
	first, err := candidateWSRead(t, conn)
	require.NoError(t, err)
	responseID := gjson.GetBytes(first, "response.id").String()
	require.NotEmpty(t, responseID)
	candidateWSWrite(t, conn, fmt.Sprintf(`{"type":"response.create","previous_response_id":%q,"input":"continue"}`, responseID))
	_, err = candidateWSRead(t, conn)
	require.NoError(t, err)
	require.NoError(t, conn.Close(coderws.StatusNormalClosure, "next key"))
	fixture.state.mu.Lock()
	fixture.state.key.ID = 21
	fixture.state.mu.Unlock()
	next := fixture.dial(t)
	candidateWSWrite(t, next, fmt.Sprintf(`{"type":"response.create","model":"gpt-5.4","previous_response_id":%q,"input":"new key continuation"}`, responseID))
	_, err = candidateWSRead(t, next)
	require.NoError(t, err)
}

func TestCandidateWebSocket_RevalidatesKeyAfterWaitingForFirstFrame(t *testing.T) {
	fixture := newCandidateWSSocketFixture(t, service.OpenAIWSIngressModePassthrough, false)
	conn := fixture.dial(t)
	fixture.state.mu.Lock()
	fixture.state.key.Status = "disabled"
	fixture.state.mu.Unlock()
	candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"hello"}`)
	_, err := candidateWSRead(t, conn)
	require.Error(t, err)
	require.Equal(t, coderws.StatusPolicyViolation, coderws.CloseStatus(err))
	select {
	case frame := <-fixture.upstream:
		t.Fatalf("revoked key frame reached upstream: %s", frame)
	default:
	}
}

func TestCandidateWebSocket_PassthroughReacquiresSlotsForEachTurn(t *testing.T) {
	for _, scope := range []string{"account", "user"} {
		t.Run(scope, func(t *testing.T) {
			fixture := newCandidateWSSocketFixture(t, service.OpenAIWSIngressModePassthrough, false)
			conn := fixture.dial(t)
			candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"hello"}`)
			_, err := candidateWSRead(t, conn)
			require.NoError(t, err)
			<-fixture.upstream
			fixture.state.mu.Lock()
			if scope == "account" {
				fixture.state.accountBusy = true
			} else {
				fixture.state.userBusy = true
			}
			fixture.state.mu.Unlock()
			candidateWSWrite(t, conn, `{"type":"response.create","model":"gpt-5.4","input":"next"}`)
			_, err = candidateWSRead(t, conn)
			require.Error(t, err)
			require.Equal(t, coderws.StatusTryAgainLater, coderws.CloseStatus(err))
			select {
			case frame := <-fixture.upstream:
				t.Fatalf("frame without slot reached upstream: %s", frame)
			default:
			}
		})
	}
}
