package protocolrouter

import (
	"context"
	"errors"
	"fmt"
)

var (
	ErrNoLegalRoute      = errors.New("no legal protocol route")
	ErrStalePlan         = errors.New("stale protocol route plan")
	ErrMissingCredential = errors.New("missing account credential")
)

type RouteAdapter interface {
	Execute(ctx context.Context, execution Execution) (Result, error)
}

type AdapterCatalog map[RouteAdapterID]RouteAdapter

type Router struct {
	adapters    map[RouteAdapterID]RouteAdapter
	registryErr error
}

func New(catalog AdapterCatalog) *Router {
	adapters := make(map[RouteAdapterID]RouteAdapter, len(catalog))
	for id, adapter := range catalog {
		if id == "" || adapter == nil {
			continue
		}
		adapters[id] = adapter
	}
	return &Router{adapters: adapters, registryErr: validateRouteRegistry(routeRegistry)}
}

type Plan struct {
	accountID       int64
	accountRevision string
	requestDigest   RequestDigest
	resolvedModel   string
	inboundProtocol Protocol
	targetProtocol  Protocol
	responsesPath   ResponsesPathKind
	endpoint        string
	adapterID       RouteAdapterID
	transport       TransportID
	routeKind       RouteKind
	geminiProfile   GeminiEndpointProfile
	reason          string
}

func (p Plan) AccountID() int64                     { return p.accountID }
func (p Plan) AccountRevision() string              { return p.accountRevision }
func (p Plan) RequestDigest() RequestDigest         { return p.requestDigest }
func (p Plan) ResolvedModel() string                { return p.resolvedModel }
func (p Plan) InboundProtocol() Protocol            { return p.inboundProtocol }
func (p Plan) TargetProtocol() Protocol             { return p.targetProtocol }
func (p Plan) ResponsesPath() ResponsesPathKind     { return p.responsesPath }
func (p Plan) Endpoint() string                     { return p.endpoint }
func (p Plan) AdapterID() RouteAdapterID            { return p.adapterID }
func (p Plan) Transport() TransportID               { return p.transport }
func (p Plan) RouteKind() RouteKind                 { return p.routeKind }
func (p Plan) Reason() string                       { return p.reason }
func (p Plan) GeminiProfile() GeminiEndpointProfile { return p.geminiProfile }

func (r *Router) Plan(request CanonicalRequest, account AccountSnapshot) (Plan, error) {
	if r == nil {
		return Plan{}, fmt.Errorf("%w: router is nil", ErrNoLegalRoute)
	}
	if r.registryErr != nil {
		return Plan{}, fmt.Errorf("%w: %v", ErrNoLegalRoute, r.registryErr)
	}
	for _, route := range routeRegistry {
		if route.inbound != request.inboundProtocol {
			continue
		}
		if !account.supports(route.target) || route.model == nil || !route.model(account) {
			continue
		}
		if !account.hasTransport(route.transport) || r.adapters[route.adapterID] == nil {
			continue
		}
		if route.preserves == nil || !route.preserves(request) {
			continue
		}
		responsesPath := ResponsesPathNone
		if route.target == ProtocolResponses {
			responsesPath = ResponsesPathRoot
			if request.inboundProtocol == ProtocolResponses {
				responsesPath = request.responsesPath
			}
		}
		if !routePermitsResponsesPath(route, responsesPath) {
			continue
		}
		endpoint, err := route.endpoint(account, route.target, responsesPath)
		if err != nil {
			continue
		}
		return Plan{
			accountID:       account.accountID,
			accountRevision: account.revision,
			requestDigest:   request.digest,
			resolvedModel:   account.resolvedModel,
			inboundProtocol: request.inboundProtocol,
			targetProtocol:  route.target,
			responsesPath:   responsesPath,
			endpoint:        endpoint,
			adapterID:       route.adapterID,
			transport:       route.transport,
			routeKind:       route.kind,
			geminiProfile:   account.geminiProfile,
			reason:          string(route.kind),
		}, nil
	}
	return Plan{}, ErrNoLegalRoute
}

type ExecutionAccountState struct {
	AccountID         int64
	Revision          string
	CredentialPresent bool
}

type executionAccountStateKey struct{}

func WithExecutionAccountState(ctx context.Context, state ExecutionAccountState) context.Context {
	return context.WithValue(ctx, executionAccountStateKey{}, state)
}

type Execution struct {
	plan    Plan
	request CanonicalRequest
}

func (e Execution) Plan() Plan                { return e.plan }
func (e Execution) Request() CanonicalRequest { return e.request }

type Result struct {
	Value any
}

func (r *Router) Execute(ctx context.Context, plan Plan, request CanonicalRequest) (Result, error) {
	if r == nil {
		return Result{}, ErrStalePlan
	}
	if plan.requestDigest != request.digest {
		return Result{}, ErrStalePlan
	}
	state, ok := ctx.Value(executionAccountStateKey{}).(ExecutionAccountState)
	if !ok || state.AccountID != plan.accountID || state.Revision != plan.accountRevision {
		return Result{}, ErrStalePlan
	}
	if !state.CredentialPresent {
		return Result{}, ErrMissingCredential
	}
	if plan.endpoint == "" || plan.adapterID == "" || plan.transport == "" {
		return Result{}, ErrStalePlan
	}
	adapter := r.adapters[plan.adapterID]
	if adapter == nil {
		return Result{}, ErrStalePlan
	}
	return adapter.Execute(ctx, Execution{plan: plan, request: request})
}
