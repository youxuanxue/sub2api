package routes

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

type terminalRoutePolicy struct {
	kind           handler.TerminalOutcomePolicy
	excludedReason string
}

var (
	SyncInference   = terminalRoutePolicy{kind: handler.TerminalOutcomeSyncInference}
	StreamInference = terminalRoutePolicy{kind: handler.TerminalOutcomeStreamInference}
	WebSocketTurn   = terminalRoutePolicy{kind: handler.TerminalOutcomeWebSocketTurn}
	AsyncSubmission = terminalRoutePolicy{kind: handler.TerminalOutcomeAsyncSubmission}
)

func Excluded(reason string) terminalRoutePolicy {
	return terminalRoutePolicy{kind: handler.TerminalOutcomeExcluded, excludedReason: strings.TrimSpace(reason)}
}

type terminalRouteRegistrar struct {
	routes gin.IRoutes
	sink   service.TerminalOutcomeSink
}

func newTerminalRouteRegistrar(routes gin.IRoutes, sink service.TerminalOutcomeSink) terminalRouteRegistrar {
	return terminalRouteRegistrar{routes: routes, sink: sink}
}

func (r terminalRouteRegistrar) Register(method, path string, policy terminalRoutePolicy, handlers ...gin.HandlerFunc) gin.IRoutes {
	if !validTerminalRoutePolicy(policy) {
		panic(fmt.Sprintf("gateway route %s %s has invalid terminal policy", method, path))
	}
	// Gin runs inherited group middleware before route handlers. Authentication
	// can resolve a universal candidate and Abort with an empty-pool 429, so a
	// route-local collector never sees that terminal. Clone the group at each
	// registration: preserve its current chain/order, wrapping it without
	// mutating siblings or maintaining another route/policy registry.
	var group *gin.RouterGroup
	switch routes := r.routes.(type) {
	case *gin.Engine:
		group = routes.Group("")
	case *gin.RouterGroup:
		group = routes.Group("")
	default:
		panic("terminal registrar requires a Gin engine or router group")
	}
	// gateway-in-flight-gate-tk owns relay admission.
	//
	// The boundary is "did this request enter the gateway relay surface", which
	// is exactly the set registered through this registrar. That makes health
	// probes, the public settings bootstrap, the panel/admin plane, setup and
	// static frontend assets exempt by construction rather than by a parallel
	// path list that can drift from the routes.
	//
	// Admission sits AFTER authentication, never before it. Two registration
	// shapes exist in this tree: most groups carry apiKeyAuth on the group, but
	// the root and codex groups pass it per route in `handlers`. Prepending the
	// gate to the group chain would therefore (a) admit unauthenticated traffic
	// in the per-route groups and (b) let an attacker hold every slot with
	// invalid API keys, turning our own limiter into the outage. Appending it to
	// `handlers` is the one placement that is uniformly post-auth in both
	// shapes.
	//
	// Cost of running after auth: a shed request pays one credential lookup
	// before it is refused. That is the deliberate trade — a bounded per-request
	// lookup beats exposing a global slot pool to unauthenticated callers.
	// Billing, routing and body streaming still never run for a shed request.
	//
	// The gate applies to Excluded routes too (model catalog, task polls,
	// content downloads, batch control). They are cheap per request, but a spike
	// of them still allocates JSON, DB cursors and response buffers, and shaping
	// total concurrency is the point of the gate.
	//
	// TerminalOutcomeMiddleware stays on the group (outside the gate), because a
	// refused admission is not a model attempt and must not be counted as one.
	if policy.kind != handler.TerminalOutcomeExcluded {
		group.Handlers = append(gin.HandlersChain{handler.TerminalOutcomeMiddleware(r.sink, policy.kind)}, group.Handlers...)
	}
	// Insert admission immediately before the business handler — the last
	// element of every registration in this tree. That position is uniformly
	// post-auth regardless of which shape the group uses, and it is still ahead
	// of billing, routing and body streaming.
	if len(handlers) == 0 {
		panic(fmt.Sprintf("gateway route %s %s has no handler", method, path))
	}
	chain := make(gin.HandlersChain, 0, len(handlers)+1)
	chain = append(chain, handlers[:len(handlers)-1]...)
	chain = append(chain, middleware2.InFlightGate, handlers[len(handlers)-1])
	return group.Handle(method, path, chain...)
}

func validTerminalRoutePolicy(policy terminalRoutePolicy) bool {
	switch policy.kind {
	case handler.TerminalOutcomeSyncInference,
		handler.TerminalOutcomeStreamInference,
		handler.TerminalOutcomeWebSocketTurn,
		handler.TerminalOutcomeAsyncSubmission:
		return policy.excludedReason == ""
	case handler.TerminalOutcomeExcluded:
		return policy.excludedReason != ""
	default:
		return false
	}
}
