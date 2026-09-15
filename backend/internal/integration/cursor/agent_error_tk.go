package cursor

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/util/logredact"
	"google.golang.org/protobuf/encoding/protowire"
	"strings"
)

// AgentRejection retains bounded diagnostics for operators. Error() deliberately
// exposes only the protocol code; supplier messages may contain request content.
type AgentRejection struct {
	Cause           *Error
	Code            string
	Diagnostic      string
	RequestID       string
	Metadata        string
	DetailInventory string
	// Structured supplier facts; never derived from echoed additional_info.
	ProviderMessage string
	ActionRequired  string
}

// Preserve error identity for the shared cancellation/transport owner without
// exposing URLs or credentials through the public error string.
type agentTransportError struct{ cause error }

func (e *agentTransportError) Error() string {
	if errors.Is(e.cause, context.Canceled) {
		return "cursor upstream transport failed: context canceled"
	}
	if errors.Is(e.cause, context.DeadlineExceeded) {
		return "cursor upstream transport failed: context deadline exceeded"
	}
	return "cursor upstream transport failed"
}
func (e *agentTransportError) Unwrap() error { return e.cause }

type agentConnectError struct {
	Code    string               `json:"code"`
	Message string               `json:"message"`
	Details []agentConnectDetail `json:"details"`
}

func newAgentHTTPRejection(status int, raw []byte, token, requestID string) *AgentRejection {
	var envelope agentConnectError
	if json.Unmarshal(raw, &envelope) == nil && envelope.Code != "" {
		return newAgentRejection(status, envelope.Code, envelope.Message, token, requestID, envelope.Details...)
	}
	// An HTML page, proxy response or echoed body is diagnostic data, never
	// structured policy evidence.
	rejection := newAgentRejection(status, "unknown", "", token, requestID)
	rejection.Diagnostic = agentBoundedErrorText(string(raw), token)
	return rejection
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
	facts := &AgentRejection{ProviderMessage: message}
	message += agentErrorDetailsText(details, token, facts)
	facts.ProviderMessage = agentBoundedErrorText(facts.ProviderMessage, token)
	facts.ActionRequired = agentBoundedErrorText(facts.ActionRequired, token)
	if token != "" {
		message = strings.ReplaceAll(message, token, "[redacted]")
	}
	message = logredact.RedactText(message)
	message = strings.Join(strings.Fields(message), " ")
	if len(message) > 2048 {
		message = strings.ToValidUTF8(message[:2048], "") + "…"
	}
	return &AgentRejection{Cause: &Error{Status: status, Message: fmt.Sprintf("cursor upstream rejected request (Connect %s, mapped HTTP %d)", code, status)}, Code: code, Diagnostic: message, RequestID: requestID, DetailInventory: agentDetailInventory(details, token), ProviderMessage: facts.ProviderMessage, ActionRequired: facts.ActionRequired}
}

// Only decode the diagnostic fields from the pinned CLI's ErrorDetails schema.
// Opaque protobuf/base64 values are inventoried without exposing their bytes.
// Known diagnostic maps and metadata are redacted and bounded separately.
// Wire owner: CLI 2026.09.02-c22c1a3, aiserver.v1.ErrorDetails (1=enum,
// 2=CustomErrorDetails); CustomErrorDetails (1=title, 2=detail, 4=is_retryable,
// 7=additional_info map<string,string>, 10=ErrorAnalyticsMetadata (1=action_required)).
type agentConnectDetail struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

func agentErrorDetailsText(details []agentConnectDetail, token string, facts ...*AgentRejection) string {
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
				_, _ = out.WriteString(agentCustomErrorText(nested, token, facts...))
			}
			raw = raw[size:]
		}
	}
	return out.String()
}
func agentCustomErrorText(raw []byte, token string, facts ...*AgentRejection) string {
	var out strings.Builder
	additional := make(map[string]string)
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
			if len(facts) > 0 {
				facts[0].ProviderMessage += " " + string(value)
			}
		}
		if num == 7 && typ == protowire.BytesType && len(additional) < 32 {
			entry, _ := protowire.ConsumeBytes(raw)
			key, value := agentDiagnosticMapEntry(entry)
			if key != "" {
				additional[key] = value
			}
		}
		if num == 10 && typ == protowire.BytesType {
			nested, _ := protowire.ConsumeBytes(raw)
			if action := agentAnalyticsActionRequired(nested); action != "" {
				fmt.Fprintf(&out, "; action_required=%s", action)
				if len(facts) > 0 {
					facts[0].ActionRequired = action
				}
			}
		}
		if num == 4 && typ == protowire.VarintType {
			value, _ := protowire.ConsumeVarint(raw)
			fmt.Fprintf(&out, "; retryable=%t", value != 0)
		}
		raw = raw[size:]
	}
	if len(additional) > 0 {
		fmt.Fprintf(&out, "; additional_info=%s", agentDiagnosticJSON(additional, token, 2048))
	}
	return out.String()
}

func agentDiagnosticMapEntry(raw []byte) (key, value string) {
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
		if typ == protowire.BytesType {
			data, _ := protowire.ConsumeBytes(raw)
			if num == 1 {
				key = string(data)
			}
			if num == 2 {
				value = string(data)
			}
		}
		raw = raw[size:]
	}
	return key, value
}

// Redact before truncating so a credential crossing the size boundary cannot leak.
func agentDiagnosticJSON(value any, token string, limit int) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return "<invalid diagnostic>"
	}
	text := string(raw)
	if token != "" {
		text = strings.ReplaceAll(text, token, "[redacted]")
	}
	text = logredact.RedactJSON([]byte(text))
	if len(text) > limit {
		text = strings.ToValidUTF8(text[:limit], "") + "…"
	}
	return text
}
func agentDetailInventory(details []agentConnectDetail, token string) string {
	inventory := make([]map[string]any, 0, min(len(details), 8))
	for i, detail := range details {
		if i >= 8 {
			break
		}
		inventory = append(inventory, map[string]any{"type": detail.Type, "encoded_bytes": len(detail.Value), "sha256": fmt.Sprintf("%x", sha256.Sum256([]byte(detail.Value))), "known_schema": detail.Type == "aiserver.v1.ErrorDetails"})
	}
	return agentDiagnosticJSON(map[string]any{"count": len(details), "details": inventory}, token, 2048)
}

// ErrorAnalyticsMetadata belongs to the pinned official ErrorDetails schema.
// Decode only the action label; redaction and bounds remain with the diagnostic
// owner. No supplier-provided action is executed or treated as retry permission.
func agentAnalyticsActionRequired(raw []byte) string {
	var action string
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
		if num == 1 && typ == protowire.BytesType {
			value, _ := protowire.ConsumeBytes(raw)
			action = string(value)
		}
		raw = raw[size:]
	}
	return action
}

func agentBoundedErrorText(value, token string) string {
	if token != "" {
		value = strings.ReplaceAll(value, token, "[redacted]")
	}
	value = strings.Join(strings.Fields(logredact.RedactText(value)), " ")
	if len(value) > 2048 {
		value = strings.ToValidUTF8(value[:2048], "") + "…"
	}
	return value
}
