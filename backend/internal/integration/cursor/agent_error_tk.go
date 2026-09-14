package cursor

import (
	"encoding/base64"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"google.golang.org/protobuf/encoding/protowire"
	"strings"
)

// AgentRejection retains bounded diagnostics for operators. Error() deliberately
// exposes only the protocol code; supplier messages may contain request content.
type AgentRejection struct {
	Cause      *Error
	Code       string
	Diagnostic string
	RequestID  string
}

func (e *AgentRejection) Error() string { return e.Cause.Error() }
func (e *AgentRejection) Unwrap() error { return e.Cause }
func newAgentRejection(status int, code, message, token, requestID string, details ...agentConnectDetail) *AgentRejection {
	// Connect defines a fixed vocabulary. Never echo an arbitrary upstream code.
	switch code {
	case "canceled", "unknown", "invalid_argument", "deadline_exceeded", "not_found", "already_exists", "permission_denied", "resource_exhausted", "failed_precondition", "aborted", "out_of_range", "unimplemented", "internal", "unavailable", "data_loss", "unauthenticated":
	default:
		code = "unknown"
	}
	message += agentErrorDetailsText(details)
	if token != "" {
		message = strings.ReplaceAll(message, token, "[redacted]")
	}
	message = logredact.RedactText(message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 2048 {
		message = strings.ToValidUTF8(message[:2048], "") + "…"
	}
	return &AgentRejection{Cause: &Error{Status: status, Message: fmt.Sprintf("cursor upstream rejected request (Connect %s, mapped HTTP %d)", code, status)}, Code: code, Diagnostic: message, RequestID: requestID}
}

// Only decode the diagnostic fields from the pinned CLI's ErrorDetails schema.
// Do not log opaque protobuf/base64 values, arbitrary maps, links or metadata.
// Wire owner: CLI 2026.09.02-c22c1a3, aiserver.v1.ErrorDetails (1=enum,
// 2=CustomErrorDetails); CustomErrorDetails (1=title, 2=detail, 4=is_retryable).
type agentConnectDetail struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func agentErrorDetailsText(details []agentConnectDetail) string {
	var out strings.Builder
	for i, detail := range details {
		if i >= 8 {
			break
		}
		if detail.Type != "aiserver.v1.ErrorDetails" || len(detail.Value) > 24<<10 {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(detail.Value)
		if err != nil {
			raw, err = base64.RawStdEncoding.DecodeString(detail.Value)
		}
		if err != nil {
			continue
		}
		for len(raw) > 0 {
			num, typ, n := protowire.ConsumeTag(raw)
			if n < 0 {
				break
			}
			raw = raw[n:]
			size := protowire.ConsumeFieldValue(num, typ, raw)
			if size < 0 {
				break
			}
			if num == 1 && typ == protowire.VarintType {
				value, _ := protowire.ConsumeVarint(raw)
				fmt.Fprintf(&out, "; supplier_error=%d", value)
			}
			if num == 2 && typ == protowire.BytesType {
				nested, _ := protowire.ConsumeBytes(raw)
				_, _ = out.WriteString(agentCustomErrorText(nested))
			}
			raw = raw[size:]
		}
	}
	return out.String()
}
func agentCustomErrorText(raw []byte) string {
	var out strings.Builder
	for len(raw) > 0 {
		num, typ, n := protowire.ConsumeTag(raw)
		if n < 0 {
			break
		}
		raw = raw[n:]
		size := protowire.ConsumeFieldValue(num, typ, raw)
		if size < 0 {
			break
		}
		if (num == 1 || num == 2) && typ == protowire.BytesType {
			value, _ := protowire.ConsumeBytes(raw)
			label := "title"
			if num == 2 {
				label = "detail"
			}
			fmt.Fprintf(&out, "; %s=%s", label, value)
		}
		if num == 4 && typ == protowire.VarintType {
			value, _ := protowire.ConsumeVarint(raw)
			fmt.Fprintf(&out, "; retryable=%t", value != 0)
		}
		raw = raw[size:]
	}
	return out.String()
}
