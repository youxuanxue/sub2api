package middleware

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"

	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

// Keep recovery observations on Gin's existing error chain. Ordinary upstream
// attempt errors must not turn a later successful failover into a failure.
type recoveredPanicError struct{ value any }

func (e recoveredPanicError) Error() string { return fmt.Sprintf("recovered panic: %v", e.value) }

// RecoveryOutcome also recognizes Gin's built-in broken-pipe recovery, which
// appends c.Errors and bypasses the custom recovery callback below.
func RecoveryOutcome(c *gin.Context) (panicked, disconnected bool) {
	for _, entry := range c.Errors {
		var recovered recoveredPanicError
		panicked = panicked || errors.As(entry.Err, &recovered)
		disconnected = disconnected || isBrokenPipe(entry.Err)
	}
	return
}

// Recovery converts panics into the project's standard JSON error envelope.
//
// It preserves Gin's broken-pipe handling by not attempting to write a response
// when the client connection is already gone.
func Recovery() gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(gin.DefaultErrorWriter, func(c *gin.Context, recovered any) {
		recoveredErr, _ := recovered.(error)

		if isBrokenPipe(recoveredErr) {
			if recoveredErr != nil {
				_ = c.Error(recoveredErr)
			}
			c.Abort()
			return
		}
		_ = c.Error(recoveredPanicError{value: recovered})

		if c.Writer.Written() {
			c.Abort()
			return
		}

		response.ErrorWithDetails(
			c,
			http.StatusInternalServerError,
			infraerrors.UnknownMessage,
			infraerrors.UnknownReason,
			nil,
		)
		c.Abort()
	})
}

func isBrokenPipe(err error) bool {
	if err == nil {
		return false
	}

	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		return false
	}

	var syscallErr *os.SyscallError
	if !errors.As(opErr.Err, &syscallErr) {
		return false
	}

	msg := strings.ToLower(syscallErr.Error())
	return strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer")
}
