package middleware

import (
	"context"
	"log"
	"net/http"
	"strconv"
	"sync/atomic"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

// gateway-in-flight-gate-tk: process-wide relay concurrency cap with fast-fail.
//
// Why this exists: a traffic spike could drive the host into the OOM killer. A
// container memory cap bounds the blast radius but converts a spike into a
// SIGKILL of every admitted request. This gate sheds *new* work before the cap
// is reached, so admitted requests keep their memory budget and finish.
//
// Semantics:
//   - Reject, never queue. Over the cap the request fails immediately with
//     503 + Retry-After. Queueing would move the spike into parked goroutines
//     and retained request bodies that still allocate — exactly what a memory
//     cap cannot absorb.
//   - Process-local counter. Each app node protects its own memory. A Redis-wide
//     counter has no safe failure mode here: fail-open loses the gate during the
//     same Redis blip that usually accompanies overload, and fail-close turns
//     Redis latency into gateway downtime. Multi-node prod stays covered because
//     every app node runs this same per-node cap.
//   - A slot is held until the response is fully written, including SSE. gin
//     runs a middleware's deferred cleanup after the handler returns, and these
//     handlers write their stream synchronously, so the release lands after the
//     last flush rather than at first byte.
//   - Shed requests are not billed and are not ops errors: the gate runs before
//     bodyLimit and apiKeyAuth, and marks an ingress reject so shared ops paths
//     classify it as capacity rather than failure.
//
// Mounting: this is not an engine-wide middleware. It is attached per relay
// route by the terminal-route registrar (routes/gateway_terminal_routes.go), so
// the gated set is exactly the inference-bearing route inventory and health
// probes, the public settings bootstrap, the panel/admin plane and static
// assets are exempt by construction instead of by a parallel path list that
// could drift.

// maxInFlightRelay is the process-wide cap. 0 (or negative) disables the gate.
var maxInFlightRelay atomic.Int64

// inflightRelay is the number of relay requests currently admitted.
var inflightRelay atomic.Int64

// inflightRelayRejected counts requests shed by the gate since start.
var inflightRelayRejected atomic.Uint64

const (
	// inFlightRetryAfterSeconds is the advisory backoff handed to shed clients.
	// One second breaks a retry storm without measurably delaying a request that
	// would otherwise have queued.
	inFlightRetryAfterSeconds = 1
	// inFlightOverloadedMessage is the single client-visible copy for a shed
	// request. It deliberately names no pool, account or internal limit.
	inFlightOverloadedMessage = "Service is temporarily overloaded. Please retry later."
)

// SetMaxInFlightRelay configures the gate. n <= 0 disables it.
// router.go is the single caller.
func SetMaxInFlightRelay(n int) {
	if n < 0 {
		n = 0
	}
	maxInFlightRelay.Store(int64(n))
	if n == 0 {
		log.Printf("[InFlightGate] disabled by configuration (gateway.max_in_flight_requests<=0)")
		return
	}
	log.Printf("[InFlightGate] enabled max_in_flight_requests=%d", n)
}

// MaxInFlightRelay returns the configured cap (0 = disabled).
func MaxInFlightRelay() int { return int(maxInFlightRelay.Load()) }

// InFlightRelayCount returns the number of admitted relay requests.
func InFlightRelayCount() int64 { return inflightRelay.Load() }

// InFlightRelayRejectedCount returns how many requests the gate has shed.
func InFlightRelayRejectedCount() uint64 { return inflightRelayRejected.Load() }

// InFlightGate is the shared handler instance. It carries no per-request state,
// so one instance keeps function identity stable — a sentinel and route-shape
// checks can recognize it in a handler chain.
var InFlightGate = newInFlightGate()

// admitInFlightRelay takes one slot if the gauge is below limit.
//
// It uses compare-and-swap rather than Add-then-rollback. Add(1) followed by
// Add(-1) on overflow never admits more than limit requests, but it transiently
// publishes a value above the limit. Concurrent observers (/health/inflight and
// the drain probe) can read that value, and a concurrent request whose Add lands
// on the inflated gauge is rejected even though a slot is actually free. CAS
// keeps the gauge strictly bounded: it never exceeds limit at any instant.
func admitInFlightRelay(limit int64) bool {
	for {
		current := inflightRelay.Load()
		if current >= limit {
			return false
		}
		if inflightRelay.CompareAndSwap(current, current+1) {
			return true
		}
	}
}

func newInFlightGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := maxInFlightRelay.Load()
		if limit <= 0 {
			// A disabled gate is not merely permissive: it never touches the
			// counter, so flipping the config off needs no bookkeeping.
			c.Next()
			return
		}
		if !admitInFlightRelay(limit) {
			inflightRelayRejected.Add(1)
			writeInFlightRejection(c)
			c.Abort()
			return
		}
		// Release exactly once, from whichever happens first:
		//   - the deferred call: normal completion, streamed completion and panic
		//     unwinding through gin's Recovery all reach it;
		//   - request-context cancellation: a handler that keeps draining for
		//     late billing after the caller disconnected must not pin a slot.
		// A leaked slot would wedge the gate permanently, so both paths share one
		// idempotent release instead of relying on ordering.
		var released atomic.Bool
		release := func() {
			if released.CompareAndSwap(false, true) {
				inflightRelay.Add(-1)
			}
		}
		stop := context.AfterFunc(c.Request.Context(), release)
		defer func() {
			stop()
			release()
		}()
		c.Next()
	}
}

// writeInFlightRejection renders the overload in the caller's own protocol
// envelope so existing SDK error handling still parses it. It deliberately does
// not annotate the busy-429 path: that would record a server-side capacity
// condition as a client quota/rate-limit event.
func writeInFlightRejection(c *gin.Context) {
	if c == nil || c.Request == nil {
		return
	}
	// Marked at write time so access logs, ops error suppression and the ingress
	// reject aggregator all see this as a capacity reject, not a platform error.
	MarkIngressRejected(c, IngressRejectInFlightSaturated)
	c.Header("Retry-After", strconv.Itoa(inFlightRetryAfterSeconds))
	switch service.TkErrorWireForRequest(c) {
	case service.TkErrorWireAnthropic:
		AnthropicErrorWriter(c, http.StatusServiceUnavailable, inFlightOverloadedMessage)
	case service.TkErrorWireGemini:
		GoogleErrorWriter(c, http.StatusServiceUnavailable, inFlightOverloadedMessage)
	default:
		writeOpenAIOverloadedError(c)
	}
}

// writeOpenAIOverloadedError emits the OpenAI error envelope with
// type/code=overloaded_error. It deliberately does not reuse OpenAIErrorWriter:
// that writer is the unsupported-model owner and would stamp
// error.code=model_not_found, mislabelling a capacity shed as a permanent model
// error that clients must not retry.
func writeOpenAIOverloadedError(c *gin.Context) {
	c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{
		"message": inFlightOverloadedMessage,
		"type":    "overloaded_error",
		"param":   nil,
		"code":    "overloaded_error",
	}})
}
