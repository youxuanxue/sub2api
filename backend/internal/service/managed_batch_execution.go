package service

// Managed Batch execution deliberately re-enters the same scheduler and
// protocol execution owner used by the synchronous Responses handler.  It
// does not construct provider HTTP requests or perform a second account
// selection inside the worker.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/batchadapter"
	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/gin-gonic/gin"
)

var (
	ErrManagedBatchExecutionUnavailable = errors.New("managed batch execution unavailable")
	ErrManagedBatchCapacity             = errors.New("interactive capacity takes precedence")
	ErrManagedBatchUnauthorized         = errors.New("managed batch item is not authorized")
	ErrManagedBatchStreamUnsupported    = errors.New("managed batch Responses items must set stream=false")
)

// ManagedBatchExecution is kept in the request context between selection and
// execution. The selection pointer is the immutable Plan produced by the
// scheduler; the runner consumes it exactly once.
type ManagedBatchExecution struct {
	APIKey       *APIKey
	Selection    *AccountSelectionResult
	Subscription *UserSubscription
}

type managedBatchExecutionContextKey struct{}

func WithManagedBatchExecution(ctx context.Context, execution ManagedBatchExecution) context.Context {
	return context.WithValue(ctx, managedBatchExecutionContextKey{}, execution)
}

func ManagedBatchExecutionFromContext(ctx context.Context) (ManagedBatchExecution, bool) {
	if ctx == nil {
		return ManagedBatchExecution{}, false
	}
	execution, ok := ctx.Value(managedBatchExecutionContextKey{}).(ManagedBatchExecution)
	return execution, ok && execution.APIKey != nil && execution.Selection != nil
}

// ManagedBatchAPIKeyLoader is the owner-scoped API-key lookup used at worker
// execution time. The concrete APIKeyService remains outside this package
// boundary so tests can use a narrow fixture.
type ManagedBatchAPIKeyLoader interface {
	GetByID(context.Context, int64) (*APIKey, error)
	UniversalResolver() *UniversalRoutingResolver
}

// ManagedBatchExecutionResolver selects one account through the normal
// load-aware scheduler and freezes its protocol Plan for the item attempt.
// Both key modes use the online candidate owner; no fallback to an unscoped selector.
type ManagedBatchExecutionResolver struct {
	APIKeys ManagedBatchAPIKeyLoader
	Gateway *OpenAIGatewayService
	Router  *protocolrouter.Router
}

func (r *ManagedBatchExecutionResolver) Resolve(
	ctx context.Context,
	req batchadapter.ExecutionRequest,
	batch batchadapter.ExecutionBatch,
	item batchadapter.ExecutionItem,
	line batchcontract.RequestLine,
) (batchadapter.ExecutionSelection, error) {
	if r == nil || r.APIKeys == nil || r.Gateway == nil || r.Router == nil {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchExecutionUnavailable
	}
	if req.UserID <= 0 || req.APIKeyID <= 0 || batch.ID == "" || req.BatchID != batch.ID || batch.UserID != req.UserID || batch.APIKeyID != req.APIKeyID ||
		item.ID <= 0 || item.BatchID != batch.ID || item.CustomID == "" || item.CustomID != line.CustomID || batch.Endpoint != line.URL {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchExecutionUnavailable
	}
	if line.URL != batchcontract.EndpointResponses || line.Method != http.MethodPost {
		return batchadapter.ExecutionSelection{}, fmt.Errorf("%w: only %s is enabled in phase one", ErrManagedBatchExecutionUnavailable, batchcontract.EndpointResponses)
	}
	var body struct {
		Model  string `json:"model"`
		Stream bool   `json:"stream"`
	}
	if err := json.Unmarshal(line.Body, &body); err != nil || strings.TrimSpace(body.Model) == "" {
		return batchadapter.ExecutionSelection{}, fmt.Errorf("%s: Responses body must include model", batchcontract.ErrorInvalidRequest)
	}
	if body.Stream {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchStreamUnsupported
	}
	key, err := r.APIKeys.GetByID(ctx, req.APIKeyID)
	if err != nil {
		return batchadapter.ExecutionSelection{}, err
	}
	if key == nil || key.UserID != req.UserID || key.ID != req.APIKeyID || !key.IsActive() || key.IsExpired() || key.IsQuotaExhausted() ||
		key.User == nil || key.User.ID != req.UserID || !key.User.IsActive() {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchUnauthorized
	}
	if !key.IsUniversal() && (key.GroupID == nil || key.Group == nil || key.Group.ID <= 0 || key.Group.ID != *key.GroupID) {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchUnauthorized
	}
	owner := r.APIKeys.UniversalResolver()
	if !owner.CandidateSchedulingEnabled() || owner.candidateGateway.billingCacheService == nil {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchExecutionUnavailable
	}
	// Candidate binding updates Group/GroupID on the key. Never let two items
	// share that mutable view, even when the loader returns a cached pointer.
	itemKey := *key
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolResponses, protocolrouter.ResponsesPathRoot, body.Model, false, line.Body)
	if err != nil {
		return batchadapter.ExecutionSelection{}, err
	}
	routingCtx := WithProtocolRouting(ctx, r.Router, request)
	routingCtx, candidate, err := owner.PrepareCandidateRequest(routingCtx, &itemKey, UniversalShapeForRequest(line.URL, line.Method), line.URL, body.Model, line.Body, "", "")
	if err != nil {
		return batchadapter.ExecutionSelection{}, managedBatchCandidateError(err)
	}
	if candidate == nil {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchExecutionUnavailable
	}
	selection, _, err := selectCandidateFromContext(routingCtx, candidateSelectOptions{transport: OpenAIUpstreamTransportAny, acquire: true})
	if err != nil {
		return batchadapter.ExecutionSelection{}, managedBatchCandidateError(err)
	}
	if selection == nil {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchExecutionUnavailable
	}
	// A wait plan carries no slot. Never dispatch Batch ahead of interactive waiters.
	if !selection.Acquired {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchCapacity
	}
	provider := managedBatchPlanProvider(selection)
	if provider == "" {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return batchadapter.ExecutionSelection{}, fmt.Errorf("%w: unsupported frozen Responses plan", ErrManagedBatchExecutionUnavailable)
	}
	if r.Gateway.concurrencyService != nil {
		waiting, err := r.Gateway.concurrencyService.GetAccountWaitingCount(ctx, selection.Account.ID)
		if err != nil || waiting > 0 {
			if selection.ReleaseFunc != nil {
				selection.ReleaseFunc()
			}
			return batchadapter.ExecutionSelection{}, ErrManagedBatchCapacity
		}
	}
	// The acquiring selection can bind a different authorized path than the
	// admission pass. Use its canonical body, billing group and subscription.
	executionCtx := WithResolvedTargetPlatform(candidate.current.ctx, selection.Account.Platform)
	// Projection only peeks at limits. Count this admitted item once, after
	// capacity/adapter checks, against the actual accounting origin.
	if err := owner.candidateGateway.billingCacheService.CheckBillingEligibility(executionCtx, itemKey.User, &itemKey, itemKey.Group, candidate.subscription, QuotaPlatform(executionCtx, &itemKey)); err != nil {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return batchadapter.ExecutionSelection{}, err
	}
	executionCtx = WithManagedBatchExecution(executionCtx, ManagedBatchExecution{APIKey: &itemKey, Selection: selection, Subscription: candidate.subscription})
	return batchadapter.ExecutionSelection{Provider: provider, AccountID: selection.Account.ID, Context: executionCtx, Release: selection.ReleaseFunc}, nil
}

func managedBatchCandidateError(err error) error {
	if errors.Is(err, ErrUniversalCapacityUnavailable) {
		return errors.Join(ErrManagedBatchCapacity, err)
	}
	return err
}

var _ interface {
	Resolve(context.Context, batchadapter.ExecutionRequest, batchadapter.ExecutionBatch, batchadapter.ExecutionItem, batchcontract.RequestLine) (batchadapter.ExecutionSelection, error)
} = (*ManagedBatchExecutionResolver)(nil)

// ManagedResponsesRunner captures the existing non-streaming Responses relay
// into the provider-neutral Batch response shape. It never re-selects an
// account; ExecuteSelectedProtocol revalidates the frozen Plan and reloads the
// authoritative account before sending.
type ManagedResponsesRunner struct {
	Router  *protocolrouter.Router
	Gateway *OpenAIGatewayService
}

func (r *ManagedResponsesRunner) Run(ctx context.Context, request batchadapter.ProviderRequest) (batchadapter.ProviderResponse, error) {
	if r == nil || r.Router == nil || r.Gateway == nil {
		return batchadapter.ProviderResponse{}, ErrManagedBatchExecutionUnavailable
	}
	executionCtx := request.ExecutionContext
	if executionCtx == nil {
		executionCtx = ctx
	}
	execution, ok := ManagedBatchExecutionFromContext(executionCtx)
	if !ok || execution.Selection.Account == nil || execution.Selection.Account.ID != request.AccountID {
		return batchadapter.ProviderResponse{}, ErrManagedBatchExecutionUnavailable
	}
	if managedBatchPlanProvider(execution.Selection) == "" {
		return batchadapter.ProviderResponse{}, fmt.Errorf("%w: unsupported frozen Responses plan", ErrManagedBatchExecutionUnavailable)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(request.Method, request.Endpoint, strings.NewReader(string(request.Body)))
	c.Request = c.Request.WithContext(executionCtx)
	c.Request.Header.Set("Content-Type", "application/json")
	// These keys are intentionally the same stable service boundary consumed by
	// the normal OpenAI relay; importing HTTP middleware here would create a
	// package cycle.
	c.Set("api_key", execution.APIKey)
	if execution.APIKey.Group != nil {
		c.Set(BridgeGinAuthContextKey, GinBridgeAuth{UserID: execution.APIKey.UserID, GroupName: execution.APIKey.Group.Name})
	}
	var value any
	var err error
	WithUsageEstimateCapture(func(c *gin.Context) {
		value, err = ExecuteSelectedProtocol(
			executionCtx,
			r.Router,
			execution.Selection,
			execution.Selection.Account,
			r.Gateway.ValidateProtocolEndpoint,
			r.Gateway.LoadProtocolExecutionAccount,
			ProtocolExecutors{
				ResponsesIdentity: func(forwardCtx context.Context, account *Account, _ protocolrouter.Plan, body protocolrouter.CanonicalRequest) (any, error) {
					return r.Gateway.ForwardAsResponsesDispatched(forwardCtx, c, account, body.Body())
				},
				ResponsesToChat: func(forwardCtx context.Context, account *Account, _ protocolrouter.Plan, body protocolrouter.CanonicalRequest) (any, error) {
					return r.Gateway.Forward(forwardCtx, c, account, body.Body())
				},
			},
		)
		result, _ := value.(*OpenAIForwardResult)
		r.Gateway.ApplyUpstreamCacheReadWriteAttribution(execution.Selection.Account, result)
		r.Gateway.ApplyOpenAIUsageEstimateFallback(executionCtx, c, execution.Selection.Account, result, request.Body, err)
	})(c)
	if err != nil {
		var upstream *UpstreamFailoverError
		if errors.As(err, &upstream) && (upstream.StatusCode == 429 || upstream.StatusCode == 503) {
			return batchadapter.ProviderResponse{StatusCode: upstream.StatusCode, Body: upstream.ResponseBody}, nil
		}
		return batchadapter.ProviderResponse{}, err
	}
	result, _ := value.(*OpenAIForwardResult)
	requestID := ""
	status := recorder.Code
	if status == 0 {
		status = http.StatusOK
	}
	response := batchadapter.ProviderResponse{StatusCode: status, RequestID: requestID, Body: append([]byte(nil), recorder.Body.Bytes()...)}
	if result != nil {
		requestID = result.RequestID
		response.RequestID = requestID
		if openAIUsageHasBillableTokens(result.Usage) {
			response.NormalizedUsage = &batchadapter.Usage{
				InputTokens:                 result.Usage.InputTokens,
				ImageInputTokens:            result.Usage.ImageInputTokens,
				ImageCacheReadTokens:        result.Usage.ImageCacheReadTokens,
				OutputTokens:                result.Usage.OutputTokens,
				CacheCreationInputTokens:    result.Usage.CacheCreationInputTokens,
				CacheCreation5mTokens:       result.Usage.CacheCreation5mTokens,
				CacheCreation1hTokens:       result.Usage.CacheCreation1hTokens,
				CacheReadInputTokens:        result.Usage.CacheReadInputTokens,
				ImageOutputTokens:           result.Usage.ImageOutputTokens,
				CacheCreation30mInputTokens: result.Usage.CacheCreation30mInputTokens,
				CacheCreation12hInputTokens: result.Usage.CacheCreation12hInputTokens,
				TotalTokens:                 result.Usage.InputTokens + result.Usage.OutputTokens,
			}
		}
		response.Model = result.Model
		response.UpstreamModel = result.UpstreamModel
		response.BillingModel = result.BillingModel
		response.BillingTier = result.BillingTier
		response.ServiceTier = result.ServiceTier
		response.ReasoningEffort = result.ReasoningEffort
		response.RequestedReasoningEffort = result.RequestedReasoningEffort
		response.UpstreamResponseModel = result.UpstreamResponseModel
		response.UpstreamResponseModelConflict = result.UpstreamResponseModelConflict
		response.UpstreamEndpoint = result.UpstreamEndpoint
		if response.UpstreamEndpoint == "" {
			response.UpstreamEndpoint = GetActualOpenAIUpstreamEndpoint(c)
		}
	}
	return response, nil
}

var _ batchadapter.ManagedRunner = (*ManagedResponsesRunner)(nil)
