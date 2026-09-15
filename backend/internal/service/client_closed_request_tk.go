package service

import (
	"context"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"
)

// PostgresCanceledByCallerMessage is the lib/pq 57014 error text emitted after
// database/sql sends the context cancellation request. lib/pq does not wrap
// context.Canceled, so the text match below is the only signal available.
const PostgresCanceledByCallerMessage = "canceling statement due to user request"

// StatusClientClosedRequest mirrors nginx's 499: the caller disconnected before
// the gateway could finish local auth/body handling. net/http has no constant.
// This is the single TokenKey-wide owner of the 499 status; handler and
// middleware layers must reference it instead of redeclaring a local constant.
const StatusClientClosedRequest = 499

// IsClientClosedRequest is the single owner of the caller-disconnect
// classification predicate (client-closed-499-ssot). Every ingress — API key
// auth, universal routing, request body reads, concurrency acquire, ops
// realtime — must consume this function instead of hand-rolling an
// errors.Is(err, context.Canceled) variant.
//
// Semantics, in precedence order:
//  1. a server-side deadline is NOT caller-owned even when the database driver
//     reports a cancellation while unwinding the query;
//  2. a wrapped context.Canceled IS caller-owned;
//  3. a canceled inbound request context IS caller-owned (a deadline on the
//     request context is not — it stays platform-owned);
//  4. lib/pq reports PostgreSQL 57014 text instead of wrapping
//     context.Canceled after database/sql sends the cancellation request.
//
// Deliberately NOT covered here (documented intentional divergences in
// docs/approved/client-closed-499-ssot.md): failover termination checks
// ctx.Err() != nil (both Canceled and DeadlineExceeded must stop retrying),
// upstream-dimension cancellation (tkUpstreamClientCanceled), and the ~40
// stream-suppression `!Canceled && !DeadlineExceeded` checks.
func IsClientClosedRequest(c *gin.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	if c != nil && c.Request != nil {
		switch requestErr := c.Request.Context().Err(); {
		case errors.Is(requestErr, context.DeadlineExceeded):
			return false
		case errors.Is(requestErr, context.Canceled):
			return true
		}
	}
	return err != nil && strings.Contains(strings.ToLower(err.Error()), PostgresCanceledByCallerMessage)
}
