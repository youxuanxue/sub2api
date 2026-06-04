package handler

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// tkNoAvailableAccountsRetryAfterSeconds is the Retry-After hint (seconds) sent
// with the empty-pool fast-fail. Short enough that a transient pool gap recovers
// quickly; long enough that clients back off instead of immediately retrying.
const tkNoAvailableAccountsRetryAfterSeconds = "5"

// tkNoAvailableAccounts is the gateway response status when a scheduling pool has
// no schedulable account ("No available accounts"). It deliberately returns 429
// (Too Many Requests) + Retry-After instead of 503, setting the Retry-After header
// as a side effect before the caller writes the body. Pass it in the status
// position of any streaming-aware error writer, e.g.:
//
//	h.handleStreamingAwareError(c, tkNoAvailableAccounts(c), "api_error", "No available accounts", streamStarted)
//
// WHY 429 not 503 — prod flood incident 2026-06 (deepseek-v4-flash empty newapi
// pool, single IP 58.213.121.60 peaking 1540 RPM):
//   - Stage0 runs a single app upstream behind Caddy. Caddy passive health lists
//     503 in unhealthy_status with max_fails=1, so one empty-pool 503 marks the
//     sole upstream "down"; with no failover target every subsequent request
//     (including the dashboard at https://api.tokenkey.dev/) stalls in
//     lb_try_duration. A flood of empty-pool 503s thus synthesizes a fake
//     backend outage and melts the node. 429 is NOT in unhealthy_status, so it
//     never trips passive health.
//   - 503 makes SDK clients retry aggressively (retry storm); 429 + Retry-After
//     makes them back off.
//
// Go evaluates call arguments left-to-right before the call, so the header is set
// before the error writer runs. For account-select failures the response has not
// started, so the header lands on the wire.
func tkNoAvailableAccounts(c *gin.Context) int {
	c.Header("Retry-After", tkNoAvailableAccountsRetryAfterSeconds)
	return http.StatusTooManyRequests
}
