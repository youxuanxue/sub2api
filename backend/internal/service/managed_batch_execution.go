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
	ErrManagedBatchUniversalKey         = errors.New("managed batch requires a direct API key")
	ErrManagedBatchStreamUnsupported    = errors.New("managed batch Responses items must set stream=false")
)

// ManagedBatchExecution is kept in the request context between selection and
// execution. The selection pointer is the immutable Plan produced by the
// scheduler; the runner consumes it exactly once.
type ManagedBatchExecution struct {
	APIKey    *APIKey
	Selection *AccountSelectionResult
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
}

// ManagedBatchExecutionResolver selects one account through the normal
// load-aware scheduler and freezes its protocol Plan for the item attempt.
// The first rollout accepts direct keys and OpenAI Responses routes only.
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
	if req.UserID <= 0 || req.APIKeyID <= 0 || batch.ID == "" || batch.UserID != req.UserID || batch.APIKeyID != req.APIKeyID {
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
	if key == nil || key.UserID != req.UserID || key.ID != req.APIKeyID || key.GroupID == nil || key.Group == nil {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchUniversalKey
	}
	request, err := protocolrouter.ParseCanonicalRequest(protocolrouter.ProtocolResponses, protocolrouter.ResponsesPathRoot, body.Model, false, line.Body)
	if err != nil {
		return batchadapter.ExecutionSelection{}, err
	}
	routingCtx := WithProtocolRouting(ctx, r.Router, request)
	selection, err := r.Gateway.SelectAccountWithLoadAwareness(routingCtx, key.GroupID, "", body.Model, nil)
	if err != nil {
		return batchadapter.ExecutionSelection{}, err
	}
	if selection == nil || selection.Account == nil {
		return batchadapter.ExecutionSelection{}, ErrManagedBatchExecutionUnavailable
	}
	provider := managedBatchProviderForAccount(selection.Account)
	if provider != batchadapter.ProviderOpenAI {
		if selection.ReleaseFunc != nil {
			selection.ReleaseFunc()
		}
		return batchadapter.ExecutionSelection{}, fmt.Errorf("%w: provider %q is not enabled in phase one", ErrManagedBatchExecutionUnavailable, provider)
	}
	executionCtx := WithManagedBatchExecution(routingCtx, ManagedBatchExecution{APIKey: key, Selection: selection})
	return batchadapter.ExecutionSelection{Provider: provider, AccountID: selection.Account.ID, Context: executionCtx, Release: selection.ReleaseFunc}, nil
}

func managedBatchProviderForAccount(account *Account) string {
	if account == nil {
		return ""
	}
	switch account.Platform {
	case PlatformOpenAI:
		return batchadapter.ProviderOpenAI
	case PlatformAntigravity:
		return batchadapter.ProviderAntigravity
	case PlatformKiro:
		return batchadapter.ProviderKiro
	default:
		return ""
	}
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
	if execution.Selection.ProtocolPlan == nil || execution.Selection.ProtocolPlan.TargetProtocol() != protocolrouter.ProtocolResponses {
		return batchadapter.ProviderResponse{}, fmt.Errorf("%w: selected plan is not Responses identity", ErrManagedBatchExecutionUnavailable)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(request.Method, request.Endpoint, strings.NewReader(string(request.Body)))
	c.Request = c.Request.WithContext(executionCtx)
	// These keys are intentionally the same stable service boundary consumed by
	// the normal OpenAI relay; importing HTTP middleware here would create a
	// package cycle.
	c.Set("api_key", execution.APIKey)
	if execution.APIKey.Group != nil {
		c.Set(BridgeGinAuthContextKey, GinBridgeAuth{UserID: execution.APIKey.UserID, GroupName: execution.APIKey.Group.Name})
	}
	value, err := ExecuteSelectedProtocol(
		executionCtx,
		r.Router,
		execution.Selection,
		execution.Selection.Account,
		r.Gateway.ValidateProtocolEndpoint,
		r.Gateway.LoadProtocolExecutionAccount,
		ProtocolExecutors{ResponsesIdentity: func(forwardCtx context.Context, account *Account, _ protocolrouter.Plan, body protocolrouter.CanonicalRequest) (any, error) {
			return r.Gateway.ForwardAsResponsesDispatched(forwardCtx, c, account, body.Body())
		}},
	)
	if err != nil {
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
		response.Model = result.Model
		response.UpstreamModel = result.UpstreamModel
		response.BillingModel = result.BillingModel
		response.ServiceTier = result.ServiceTier
		response.ReasoningEffort = result.ReasoningEffort
		response.RequestedReasoningEffort = result.RequestedReasoningEffort
		response.UpstreamResponseModel = result.UpstreamResponseModel
		response.UpstreamResponseModelConflict = result.UpstreamResponseModelConflict
		response.UpstreamEndpoint = result.UpstreamEndpoint
	}
	return response, nil
}

var _ batchadapter.ManagedRunner = (*ManagedResponsesRunner)(nil)
