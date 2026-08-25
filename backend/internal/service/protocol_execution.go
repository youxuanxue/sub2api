package service

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

var ErrProtocolExecutorMissing = errors.New("protocol executor missing")

type ProtocolExecutionFunc func(
	ctx context.Context,
	plan protocolrouter.Plan,
	request protocolrouter.CanonicalRequest,
) (any, error)

type ProtocolEndpointValidator func(ctx context.Context, account *Account, endpoint string) error

type RouteFacts struct {
	targetProtocol protocolrouter.Protocol
	endpoint       string
	resolvedModel  string
}

func (f RouteFacts) TargetProtocol() protocolrouter.Protocol { return f.targetProtocol }
func (f RouteFacts) Endpoint() string                        { return f.endpoint }
func (f RouteFacts) ResolvedModel() string                   { return f.resolvedModel }
func (f RouteFacts) UpstreamEndpoint() string {
	parsed, err := url.Parse(strings.TrimSpace(f.endpoint))
	if err != nil || parsed.Path == "" {
		return strings.TrimSpace(f.endpoint)
	}
	return parsed.Path
}

func (f RouteFacts) valid() bool {
	return f.targetProtocol.Valid() && f.endpoint != "" && f.resolvedModel != ""
}

func routeFactsFromPlan(plan protocolrouter.Plan) RouteFacts {
	return RouteFacts{
		targetProtocol: plan.TargetProtocol(),
		endpoint:       plan.Endpoint(),
		resolvedModel:  plan.ResolvedModel(),
	}
}

type ProtocolExecutors struct {
	NonGoverned ProtocolExecutionFunc

	MessagesIdentity    ProtocolExecutionFunc
	MessagesToResponses ProtocolExecutionFunc
	MessagesToChat      ProtocolExecutionFunc
	ChatIdentity        ProtocolExecutionFunc
	ChatToResponses     ProtocolExecutionFunc
	ChatToMessages      ProtocolExecutionFunc
	ResponsesIdentity   ProtocolExecutionFunc
	ResponsesToChat     ProtocolExecutionFunc
	ResponsesToMessages ProtocolExecutionFunc
}

type protocolExecutorsContextKey struct{}

type protocolExecutionPlanContextKey struct{}

func WithProtocolExecutors(ctx context.Context, executors ProtocolExecutors) context.Context {
	return context.WithValue(ctx, protocolExecutorsContextKey{}, executors)
}

func withProtocolExecutionPlan(ctx context.Context, plan protocolrouter.Plan) context.Context {
	return context.WithValue(ctx, protocolExecutionPlanContextKey{}, plan)
}

// ProtocolExecutionPlan returns the immutable route selected by the scheduler.
// Transport/converter code may inspect this value, but must never replace it.
func ProtocolExecutionPlan(ctx context.Context) (protocolrouter.Plan, bool) {
	plan, ok := ctx.Value(protocolExecutionPlanContextKey{}).(protocolrouter.Plan)
	return plan, ok && plan.TargetProtocol().Valid()
}

func ProtocolRouteFactsFromContext(ctx context.Context) (RouteFacts, bool) {
	plan, ok := ProtocolExecutionPlan(ctx)
	if !ok {
		return RouteFacts{}, false
	}
	facts := routeFactsFromPlan(plan)
	return facts, facts.valid()
}

func protocolExecutionTarget(ctx context.Context) (protocolrouter.Protocol, bool) {
	plan, ok := ProtocolExecutionPlan(ctx)
	if !ok {
		return "", false
	}
	return plan.TargetProtocol(), true
}

func protocolExecutionResolvedModel(ctx context.Context, fallback string) string {
	plan, ok := ProtocolExecutionPlan(ctx)
	if !ok || plan.ResolvedModel() == "" {
		return fallback
	}
	return plan.ResolvedModel()
}

func protocolExecutionEndpoint(ctx context.Context, fallback string) string {
	plan, ok := ProtocolExecutionPlan(ctx)
	if !ok || plan.Endpoint() == "" {
		return fallback
	}
	return plan.Endpoint()
}

func protocolExecutionBound(ctx context.Context) bool {
	_, ok := ProtocolExecutionPlan(ctx)
	return ok
}

func executeBoundProtocolAdapter(
	ctx context.Context,
	execution protocolrouter.Execution,
	expectedID protocolrouter.RouteAdapterID,
	expectedInbound protocolrouter.Protocol,
	expectedTarget protocolrouter.Protocol,
	execute ProtocolExecutionFunc,
) (protocolrouter.Result, error) {
	plan := execution.Plan()
	if plan.AdapterID() != expectedID || plan.InboundProtocol() != expectedInbound || plan.TargetProtocol() != expectedTarget {
		return protocolrouter.Result{}, fmt.Errorf(
			"%w: adapter %q received plan %q (%s -> %s)",
			protocolrouter.ErrStalePlan,
			expectedID,
			plan.AdapterID(),
			plan.InboundProtocol(),
			plan.TargetProtocol(),
		)
	}
	if execute == nil {
		return protocolrouter.Result{}, ErrProtocolExecutorMissing
	}
	value, err := execute(withProtocolExecutionPlan(ctx, plan), plan, execution.Request())
	if err != nil {
		return protocolrouter.Result{}, err
	}
	return protocolrouter.Result{Value: value}, nil
}

func protocolExecutorsFromContext(ctx context.Context) ProtocolExecutors {
	executors, _ := ctx.Value(protocolExecutorsContextKey{}).(ProtocolExecutors)
	return executors
}

type messagesIdentityAdapter struct{}

func (messagesIdentityAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterMessagesIdentity, protocolrouter.ProtocolMessages, protocolrouter.ProtocolMessages, protocolExecutorsFromContext(ctx).MessagesIdentity)
}

type messagesToResponsesAdapter struct{}

func (messagesToResponsesAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterMessagesToResponses, protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses, protocolExecutorsFromContext(ctx).MessagesToResponses)
}

type messagesToChatAdapter struct{}

func (messagesToChatAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterMessagesToChat, protocolrouter.ProtocolMessages, protocolrouter.ProtocolChatCompletions, protocolExecutorsFromContext(ctx).MessagesToChat)
}

type chatIdentityAdapter struct{}

func (chatIdentityAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterChatIdentity, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolChatCompletions, protocolExecutorsFromContext(ctx).ChatIdentity)
}

type chatToResponsesAdapter struct{}

func (chatToResponsesAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterChatToResponses, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolResponses, protocolExecutorsFromContext(ctx).ChatToResponses)
}

type chatToMessagesAdapter struct{}

func (chatToMessagesAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterChatToMessages, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolMessages, protocolExecutorsFromContext(ctx).ChatToMessages)
}

type responsesIdentityAdapter struct{}

func (responsesIdentityAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterResponsesIdentity, protocolrouter.ProtocolResponses, protocolrouter.ProtocolResponses, protocolExecutorsFromContext(ctx).ResponsesIdentity)
}

type responsesToChatAdapter struct{}

func (responsesToChatAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterResponsesToChat, protocolrouter.ProtocolResponses, protocolrouter.ProtocolChatCompletions, protocolExecutorsFromContext(ctx).ResponsesToChat)
}

type responsesToMessagesAdapter struct{}

func (responsesToMessagesAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterResponsesToMessages, protocolrouter.ProtocolResponses, protocolrouter.ProtocolMessages, protocolExecutorsFromContext(ctx).ResponsesToMessages)
}

func NewProtocolRouter() *protocolrouter.Router {
	return protocolrouter.New(protocolrouter.AdapterCatalog{
		protocolrouter.AdapterMessagesIdentity:    messagesIdentityAdapter{},
		protocolrouter.AdapterMessagesToResponses: messagesToResponsesAdapter{},
		protocolrouter.AdapterMessagesToChat:      messagesToChatAdapter{},
		protocolrouter.AdapterChatIdentity:        chatIdentityAdapter{},
		protocolrouter.AdapterChatToResponses:     chatToResponsesAdapter{},
		protocolrouter.AdapterChatToMessages:      chatToMessagesAdapter{},
		protocolrouter.AdapterResponsesIdentity:   responsesIdentityAdapter{},
		protocolrouter.AdapterResponsesToChat:     responsesToChatAdapter{},
		protocolrouter.AdapterResponsesToMessages: responsesToMessagesAdapter{},
	})
}

func ExecuteSelectedProtocol(
	ctx context.Context,
	router *protocolrouter.Router,
	selection *AccountSelectionResult,
	account *Account,
	validateEndpoint ProtocolEndpointValidator,
	executors ProtocolExecutors,
) (any, error) {
	request, canonical := protocolRoutingCanonicalRequest(ctx)
	_, routed := ProtocolRoutingRequest(ctx)
	plan, planned := ProtocolPlanFromSelection(selection)
	if !protocolRoutingGovernsAccount(account) {
		if executors.NonGoverned == nil {
			return nil, ErrProtocolExecutorMissing
		}
		return executors.NonGoverned(ctx, protocolrouter.Plan{}, request)
	}
	if router == nil && canonical && !routed && !planned {
		if executors.NonGoverned == nil {
			return nil, ErrProtocolExecutorMissing
		}
		return executors.NonGoverned(ctx, protocolrouter.Plan{}, request)
	}
	if !routed || !planned || router == nil {
		return nil, fmt.Errorf("%w: governed account requires canonical request, router, and selected plan", ErrProtocolRouteUnavailable)
	}
	if validateEndpoint == nil {
		return nil, fmt.Errorf("%w: governed account requires endpoint validation", ErrProtocolRouteUnavailable)
	}
	if err := validateEndpoint(ctx, account, plan.Endpoint()); err != nil {
		return nil, fmt.Errorf("%w: validate selected endpoint: %v", ErrProtocolRouteUnavailable, err)
	}
	fresh, err := protocolAccountSnapshotForRequest(account, request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProtocolRouteUnavailable, err)
	}
	executionCtx := WithProtocolExecutors(ctx, executors)
	executionCtx = protocolrouter.WithExecutionAccountState(executionCtx, protocolrouter.ExecutionAccountState{
		AccountID:         fresh.AccountID(),
		Revision:          fresh.Revision(),
		CredentialPresent: protocolCredentialPresent(account),
	})
	result, err := router.Execute(executionCtx, plan, request)
	if err != nil {
		return nil, err
	}
	stampProtocolRouteFacts(result.Value, routeFactsFromPlan(plan))
	return result.Value, nil
}

func stampProtocolRouteFacts(value any, facts RouteFacts) {
	if !facts.valid() {
		return
	}
	switch result := value.(type) {
	case *ForwardResult:
		if result != nil {
			result.protocolRouteFacts = facts
		}
	case *OpenAIForwardResult:
		if result != nil {
			result.protocolRouteFacts = facts
		}
	}
}

func protocolCredentialPresent(account *Account) bool {
	if account == nil {
		return false
	}
	if account.ParentAccountID != nil {
		return true
	}
	return len(account.Credentials) > 0
}

// ForwardResultFromOpenAI keeps the Anthropic-facing handler's accounting
// contract when a route adapter executes through OpenAIGatewayService.
func ForwardResultFromOpenAI(result *OpenAIForwardResult) *ForwardResult {
	if result == nil {
		return nil
	}
	imageSizeBreakdown := make(map[string]int, len(result.ImageSizeBreakdown))
	for size, count := range result.ImageSizeBreakdown {
		imageSizeBreakdown[size] = count
	}
	return &ForwardResult{
		RequestID: result.RequestID,
		Usage: ClaudeUsage{
			InputTokens:              result.Usage.InputTokens,
			OutputTokens:             result.Usage.OutputTokens,
			CacheCreationInputTokens: result.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     result.Usage.CacheReadInputTokens,
			ImageOutputTokens:        result.Usage.ImageOutputTokens,
		},
		Model:                         result.Model,
		UpstreamModel:                 result.UpstreamModel,
		UpstreamResponseModel:         result.UpstreamResponseModel,
		UpstreamResponseModelConflict: result.UpstreamResponseModelConflict,
		Stream:                        result.Stream,
		Duration:                      result.Duration,
		FirstTokenMs:                  result.FirstTokenMs,
		ClientDisconnect:              result.ClientDisconnect,
		ReasoningEffort:               result.ReasoningEffort,
		ServiceTier:                   result.ServiceTier,
		ImageCount:                    result.ImageCount,
		ImageSize:                     result.ImageSize,
		ImageInputSize:                result.ImageInputSize,
		ImageOutputSize:               result.ImageOutputSize,
		ImageOutputSizes:              append([]string(nil), result.ImageOutputSizes...),
		ImageSizeSource:               result.ImageSizeSource,
		ImageSizeBreakdown:            imageSizeBreakdown,
		SearchCount:                   result.SearchCount,
		AudioUsage:                    result.AudioUsage,
		protocolRouteFacts:            result.protocolRouteFacts,
	}
}
