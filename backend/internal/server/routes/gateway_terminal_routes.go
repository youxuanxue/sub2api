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
	if policy.kind != handler.TerminalOutcomeExcluded {
		chain := make([]gin.HandlerFunc, 0, len(handlers)+1)
		chain = append(chain, handler.TerminalOutcomeMiddleware(r.sink, policy.kind))
		chain = append(chain, handlers...)
		handlers = chain
	}
	return r.routes.Handle(method, path, handlers...)
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
