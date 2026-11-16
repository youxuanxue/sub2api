package server

import (
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// gatewayInFlightGauge adapts the process-local relay gate for the ops alert
// evaluator without letting service import middleware (import cycle).
type gatewayInFlightGauge struct{}

func (gatewayInFlightGauge) Count() int64 { return middleware.InFlightRelayCount() }
func (gatewayInFlightGauge) Max() int     { return middleware.MaxInFlightRelay() }

// ProvideGatewayInFlightGauge wires the relay in-flight gauge into ops alerts.
func ProvideGatewayInFlightGauge() service.GatewayInFlightGauge {
	return gatewayInFlightGauge{}
}
