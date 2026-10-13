package routes

import (
	"fmt"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/handler"
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
	if policy.kind == handler.TerminalOutcomeExcluded {
		return r.routes.Handle(method, path, handlers...)
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
	group.Handlers = append(gin.HandlersChain{handler.TerminalOutcomeMiddleware(r.sink, policy.kind)}, group.Handlers...)
	return group.Handle(method, path, handlers...)
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
