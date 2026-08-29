package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

var ErrProtocolExecutorMissing = errors.New("protocol executor missing")

const ProtocolAuthorizationSnapshotCredentialKey = "__tk_protocol_authorization_present"

const (
	protocolExecutionStaleReason      GatewayFailureReason = "protocol_execution_stale"
	protocolExecutionReloadFailReason GatewayFailureReason = "protocol_execution_reload_failed"
)

type ProtocolExecutionFunc func(
	ctx context.Context,
	account *Account,
	plan protocolrouter.Plan,
	request protocolrouter.CanonicalRequest,
) (any, error)

// ExecuteGeminiProtocolProfile binds a plan-derived Gemini profile to exactly
// one transport. Handlers supply request-scoped closures but do not select the
// provider profile themselves.
func ExecuteGeminiProtocolProfile[T any](
	profile protocolrouter.GeminiEndpointProfile,
	antigravity func() (T, error),
	vertex func() (T, error),
) (T, error) {
	var zero T
	var execute func() (T, error)
	switch profile {
	case protocolrouter.GeminiEndpointAntigravityCloudCode:
		execute = antigravity
	case protocolrouter.GeminiEndpointVertexServiceAccount:
		execute = vertex
	default:
		return zero, ErrProtocolRouteUnavailable
	}
	if execute == nil {
		return zero, ErrProtocolRouteUnavailable
	}
	return execute()
}

type ProtocolEndpointValidator func(ctx context.Context, account *Account, endpoint string) error

type ProtocolExecutionAccountLoader func(ctx context.Context, accountID int64) (*Account, error)

func protocolExecutionPreSendFailure(cause error, scope GatewayFailureScope) error {
	reason := protocolExecutionStaleReason
	nextAccountAction := NextAccountRetry
	if scope == GatewayFailureScopeProvider {
		reason = protocolExecutionReloadFailReason
		nextAccountAction = NextAccountStop
	}
	return errors.Join(
		&UpstreamFailoverError{
			StatusCode:        http.StatusServiceUnavailable,
			Stage:             GatewayFailureStageInference,
			Scope:             scope,
			Reason:            reason,
			NextAccountAction: nextAccountAction,
			ClientStatusCode:  http.StatusServiceUnavailable,
			ClientErrorType:   "server_error",
			ClientMessage:     "Service temporarily unavailable",
		},
		cause,
	)
}

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
	MessagesToGemini    ProtocolExecutionFunc
	ChatToGemini        ProtocolExecutionFunc
	ResponsesToGemini   ProtocolExecutionFunc
	GeminiIdentity      ProtocolExecutionFunc
}

type protocolExecutorsContextKey struct{}

type protocolExecutionPlanContextKey struct{}

type protocolExecutionAccountContextKey struct{}

func WithProtocolExecutors(ctx context.Context, executors ProtocolExecutors) context.Context {
	return context.WithValue(ctx, protocolExecutorsContextKey{}, executors)
}

func withProtocolExecutionPlan(ctx context.Context, plan protocolrouter.Plan) context.Context {
	return context.WithValue(ctx, protocolExecutionPlanContextKey{}, plan)
}

func withProtocolExecutionAccount(ctx context.Context, account *Account) context.Context {
	return context.WithValue(ctx, protocolExecutionAccountContextKey{}, account)
}

func protocolExecutionAccountFromContext(ctx context.Context) *Account {
	account, _ := ctx.Value(protocolExecutionAccountContextKey{}).(*Account)
	return account
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

func protocolExecutionURL(ctx context.Context, fallback string) string {
	planned := strings.TrimSpace(protocolExecutionEndpoint(ctx, ""))
	if planned == "" {
		return fallback
	}
	plannedURL, err := url.Parse(planned)
	if err != nil {
		return fallback
	}
	fallbackURL, err := url.Parse(strings.TrimSpace(fallback))
	if err == nil {
		plannedURL.RawQuery = fallbackURL.RawQuery
	}
	return plannedURL.String()
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
	executionAccount := protocolExecutionAccountFromContext(ctx)
	if executionAccount == nil {
		return protocolrouter.Result{}, fmt.Errorf("%w: authoritative execution account is missing", ErrProtocolRouteUnavailable)
	}
	value, err := execute(withProtocolExecutionPlan(ctx, plan), executionAccount, plan, execution.Request())
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

type messagesToGeminiAdapter struct{}

func (messagesToGeminiAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterMessagesToGemini, protocolrouter.ProtocolMessages, protocolrouter.ProtocolGeminiGenerateContent, protocolExecutorsFromContext(ctx).MessagesToGemini)
}

type chatToGeminiAdapter struct{}

func (chatToGeminiAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterChatToGemini, protocolrouter.ProtocolChatCompletions, protocolrouter.ProtocolGeminiGenerateContent, protocolExecutorsFromContext(ctx).ChatToGemini)
}

type responsesToGeminiAdapter struct{}

func (responsesToGeminiAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterResponsesToGemini, protocolrouter.ProtocolResponses, protocolrouter.ProtocolGeminiGenerateContent, protocolExecutorsFromContext(ctx).ResponsesToGemini)
}

type geminiIdentityAdapter struct{}

func (geminiIdentityAdapter) Execute(ctx context.Context, execution protocolrouter.Execution) (protocolrouter.Result, error) {
	return executeBoundProtocolAdapter(ctx, execution, protocolrouter.AdapterGeminiIdentity, protocolrouter.ProtocolGeminiGenerateContent, protocolrouter.ProtocolGeminiGenerateContent, protocolExecutorsFromContext(ctx).GeminiIdentity)
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
		protocolrouter.AdapterMessagesToGemini:    messagesToGeminiAdapter{},
		protocolrouter.AdapterChatToGemini:        chatToGeminiAdapter{},
		protocolrouter.AdapterResponsesToGemini:   responsesToGeminiAdapter{},
		protocolrouter.AdapterGeminiIdentity:      geminiIdentityAdapter{},
	})
}

func ExecuteSelectedProtocol(
	ctx context.Context,
	router *protocolrouter.Router,
	selection *AccountSelectionResult,
	account *Account,
	validateEndpoint ProtocolEndpointValidator,
	loadAccount ProtocolExecutionAccountLoader,
	executors ProtocolExecutors,
) (any, error) {
	request, canonical := protocolRoutingCanonicalRequest(ctx)
	_, routed := ProtocolRoutingRequest(ctx)
	plan, planned := ProtocolPlanFromSelection(selection)
	if !protocolRoutingGovernsAccount(account) {
		if executors.NonGoverned == nil {
			return nil, ErrProtocolExecutorMissing
		}
		return executors.NonGoverned(ctx, account, protocolrouter.Plan{}, request)
	}
	if router == nil && canonical && !routed && !planned {
		if executors.NonGoverned == nil {
			return nil, ErrProtocolExecutorMissing
		}
		return executors.NonGoverned(ctx, account, protocolrouter.Plan{}, request)
	}
	if !routed || !planned || router == nil {
		return nil, fmt.Errorf("%w: governed account requires canonical request, router, and selected plan", ErrProtocolRouteUnavailable)
	}
	if validateEndpoint == nil {
		return nil, fmt.Errorf("%w: governed account requires endpoint validation", ErrProtocolRouteUnavailable)
	}
	if loadAccount == nil {
		return nil, fmt.Errorf("%w: governed account requires authoritative account reload", ErrProtocolRouteUnavailable)
	}
	freshAccount, err := loadAccount(ctx, account.ID)
	if err != nil {
		scope := GatewayFailureScopeProvider
		if errors.Is(err, ErrAccountNotFound) {
			scope = GatewayFailureScopeAccount
		}
		return nil, protocolExecutionPreSendFailure(
			fmt.Errorf("%w: reload authoritative account: %w", ErrProtocolRouteUnavailable, err),
			scope,
		)
	}
	if freshAccount == nil || freshAccount.ID != account.ID {
		return nil, protocolExecutionPreSendFailure(
			fmt.Errorf("%w: authoritative account is missing or mismatched", ErrProtocolRouteUnavailable),
			GatewayFailureScopeAccount,
		)
	}
	if err := validateEndpoint(ctx, freshAccount, plan.Endpoint()); err != nil {
		return nil, protocolExecutionPreSendFailure(
			fmt.Errorf("%w: validate selected endpoint: %v", ErrProtocolRouteUnavailable, err),
			GatewayFailureScopeAccount,
		)
	}
	fresh, err := protocolAccountSnapshotForRequestWithThinking(freshAccount, request, thinkingEnabledFromCtx(ctx))
	if err != nil {
		return nil, protocolExecutionPreSendFailure(
			fmt.Errorf("%w: %v", ErrProtocolRouteUnavailable, err),
			GatewayFailureScopeAccount,
		)
	}
	executionCtx := WithProtocolExecutors(ctx, executors)
	executionCtx = withProtocolExecutionAccount(executionCtx, freshAccount)
	executionCtx = protocolrouter.WithExecutionAccountState(executionCtx, protocolrouter.ExecutionAccountState{
		AccountID:          fresh.AccountID(),
		Revision:           fresh.Revision(),
		CapabilityKey:      fresh.CapabilityKey(),
		CapabilityRevision: fresh.CapabilityRevision(),
		CredentialPresent:  ProtocolAuthorizationPresent(freshAccount),
	})
	result, err := router.Execute(executionCtx, plan, request)
	if err != nil {
		if errors.Is(err, protocolrouter.ErrStalePlan) || errors.Is(err, protocolrouter.ErrMissingCredential) {
			return nil, protocolExecutionPreSendFailure(err, GatewayFailureScopeAccount)
		}
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

func ProtocolAuthorizationPresent(account *Account) bool {
	if account == nil {
		return false
	}
	if present, ok := account.Credentials[ProtocolAuthorizationSnapshotCredentialKey].(bool); ok {
		return present
	}
	if account.ParentAccountID != nil {
		return true
	}
	if protocolGeminiEndpointProfile(account) == protocolrouter.GeminiEndpointAntigravityCloudCode {
		return strings.TrimSpace(account.GetCredential("access_token")) != "" &&
			strings.TrimSpace(account.GetCredential("project_id")) != ""
	}
	if account.IsNewAPIVertexServiceAccount() {
		_, err := parseVertexServiceAccountKey(account)
		return err == nil
	}
	return strings.TrimSpace(protocolAuthorizationToken(account)) != ""
}

func protocolRuntimeAuthorizationReady(ctx context.Context, account *Account) bool {
	if _, routed := ProtocolRoutingRequest(ctx); !routed {
		return true
	}
	return !protocolRoutingGovernsAccount(account) || ProtocolAuthorizationPresent(account)
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

// OpenAIForwardResultFromForward keeps the OpenAI-facing handler's accounting
// contract when a Gemini route adapter reuses an Anthropic-facing transport.
func OpenAIForwardResultFromForward(result *ForwardResult) *OpenAIForwardResult {
	if result == nil {
		return nil
	}
	imageSizeBreakdown := make(map[string]int, len(result.ImageSizeBreakdown))
	for size, count := range result.ImageSizeBreakdown {
		imageSizeBreakdown[size] = count
	}
	return &OpenAIForwardResult{
		RequestID: result.RequestID,
		Usage: OpenAIUsage{
			InputTokens:              result.Usage.InputTokens,
			OutputTokens:             result.Usage.OutputTokens,
			CacheCreationInputTokens: result.Usage.CacheCreationInputTokens,
			CacheReadInputTokens:     result.Usage.CacheReadInputTokens,
			ImageOutputTokens:        result.Usage.ImageOutputTokens,
		},
		Model:                         result.Model,
		BillingModel:                  result.Model,
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
