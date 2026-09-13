//go:build unit

package handler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/stretchr/testify/require"
)

// TestProtocolAvailabilityFailure_NilGuards proves the nil receivers must not panic
// (callers in production share the helper across many handler paths; one
// segfault in a forward error path would brick that handler).
func TestProtocolAvailabilityFailure_NilGuards(t *testing.T) {
	require.NotPanics(t, func() {
		recordProtocolFailureForTest(nil, context.Background(), "openai", "gpt-4o", 1, errors.New("any"))
	})
	// non-nil svc + nil err = no-op (no spurious record)
	require.NotPanics(t, func() {
		recordProtocolFailureForTest(&service.GatewayService{}, context.Background(), "openai", "gpt-4o", 1, nil)
	})
}

// Preserve upstream status/body classification through wrapped executor errors.
func TestProtocolAvailabilityFailure_ExtractsAndClassifies(t *testing.T) {
	cases := []struct {
		name              string
		statusCode        int
		responseBody      string
		expectedStatus    string
		expectedKind      string
		expectedSampleTot int
	}{
		{
			name:              "real Gemini 404 body flips to unreachable in 1 sample",
			statusCode:        404,
			responseBody:      `{"error":{"code":404,"message":"Requested entity was not found.","status":"NOT_FOUND"}}`,
			expectedStatus:    service.AvailabilityStatusUnreachable,
			expectedKind:      service.FailureKindModelNotFound,
			expectedSampleTot: 1,
		},
		{
			name:              "OpenAI 404 with model_not_found code flips to unreachable",
			statusCode:        404,
			responseBody:      `{"error":{"message":"The model 'gpt-9' does not exist","type":"invalid_request_error","code":"model_not_found"}}`,
			expectedStatus:    service.AvailabilityStatusUnreachable,
			expectedKind:      service.FailureKindModelNotFound,
			expectedSampleTot: 1,
		},
		{
			name:              "429 stays inconclusive (status unchanged from zero, no sample bump)",
			statusCode:        429,
			responseBody:      `{"error":{"message":"Rate limit exceeded"}}`,
			expectedStatus:    "", // §1.3: rate_limited leaves Status untouched; first observation of a never-written cell = zero value
			expectedKind:      service.FailureKindRateLimited,
			expectedSampleTot: 0, // §1.3 invariant: rate-limited MUST NOT pollute sample counts
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := newCapturedRepo()
			availSvc := service.NewPricingAvailabilityService(repo, time.Now)
			gw := &service.GatewayService{}
			gw.SetPricingAvailabilityService(availSvc)

			foErr := &service.UpstreamFailoverError{
				StatusCode:   tc.statusCode,
				ResponseBody: []byte(tc.responseBody),
			}
			wrapped := fmt.Errorf("forward failed at attempt 2: %w", foErr)

			recordProtocolFailureForTest(gw, context.Background(), "openai", "gpt-9", 42, wrapped)

			state := repo.get("openai", "gpt-9")
			require.Equal(t, tc.expectedStatus, state.Status, "availability status")
			require.Equal(t, tc.expectedKind, state.LastFailureKind, "failure kind")
			require.Equal(t, tc.expectedSampleTot, state.SampleTotal24h, "sample_total_24h")
			require.NotNil(t, state.UpstreamStatusCodeLast)
			require.Equal(t, tc.statusCode, *state.UpstreamStatusCodeLast)
		})
	}
}

// Local errors without upstream evidence must not affect model health.
func TestProtocolAvailabilityFailure_FallbackForNonFailoverError(t *testing.T) {
	repo := newCapturedRepo()
	availSvc := service.NewPricingAvailabilityService(repo, time.Now)
	gw := &service.GatewayService{}
	gw.SetPricingAvailabilityService(availSvc)

	plain := errors.New("connection refused before forward")
	recordProtocolFailureForTest(gw, context.Background(), "openai", "gpt-9", 7, plain)

	state := repo.get("openai", "gpt-9")
	require.Equal(t, "", state.LastFailureKind)
	// No upstream status was observed → UpstreamStatusCodeLast must remain unset.
	require.Nil(t, state.UpstreamStatusCodeLast)
}

// capturedRepo is a tiny in-memory ModelAvailabilityRepository for handler-side
// tests. Mirrors the service-package memoryAvailabilityRepo without exporting it.
type capturedRepo struct {
	mu   sync.Mutex
	rows map[string]service.AvailabilityState
}

func newCapturedRepo() *capturedRepo {
	return &capturedRepo{rows: map[string]service.AvailabilityState{}}
}

func (r *capturedRepo) key(p, m string) string { return p + "::" + m }

func (r *capturedRepo) Upsert(_ context.Context, p, m string, fn func(service.AvailabilityState) service.AvailabilityState) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	cur := r.rows[r.key(p, m)]
	r.rows[r.key(p, m)] = fn(cur)
	return nil
}

func (r *capturedRepo) Get(_ context.Context, p, m string) (service.AvailabilityState, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rows[r.key(p, m)], nil
}

func (r *capturedRepo) get(p, m string) service.AvailabilityState {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rows[r.key(p, m)]
}

func recordProtocolFailureForTest(svc *service.GatewayService, ctx context.Context, platform, model string, accountID int64, err error) {
	request, _ := protocolrouter.NewCanonicalRequest(protocolrouter.CanonicalRequestInput{InboundProtocol: protocolrouter.ProtocolChatCompletions, RequestedModel: model, Body: []byte(`{}`)})
	svc.TKRecordProtocolOutcome(ctx, &service.Account{ID: accountID, Platform: platform}, protocolrouter.Plan{}, request.RequestedModel(), nil, err)
}
