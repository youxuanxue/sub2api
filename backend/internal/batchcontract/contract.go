// Package batchcontract owns the provider-neutral Managed Batch contract.
//
// The package deliberately has no HTTP, database, queue, or billing
// dependencies. Handlers, persistence, and workers must use these values as
// their shared source of truth for the OpenAI-compatible Batch API.
package batchcontract

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
)

const (
	EndpointChatCompletions = "/v1/chat/completions"
	EndpointResponses       = "/v1/responses"
)

const (
	FilePurposeBatch    = "batch"
	FileStatusUploaded  = "uploaded"
	FileStatusProcessed = "processed"
	FileStatusError     = "error"
)

const (
	ExecutionModePending = "pending"
	ExecutionModeNative  = "native"
	ExecutionModeManaged = "managed"
)

const (
	BatchStatusValidating = "validating"
	BatchStatusInProgress = "in_progress"
	BatchStatusFinalizing = "finalizing"
	BatchStatusCompleted  = "completed"
	BatchStatusFailed     = "failed"
	BatchStatusExpired    = "expired"
	BatchStatusCancelling = "cancelling"
	BatchStatusCancelled  = "cancelled"
)

const (
	ItemStatusPending   = "pending"
	ItemStatusRunning   = "running"
	ItemStatusSucceeded = "succeeded"
	ItemStatusFailed    = "failed"
	ItemStatusCancelled = "cancelled"
	ItemStatusUnknown   = "unknown"
)

const (
	ShardStatusPending   = "pending"
	ShardStatusRunning   = "running"
	ShardStatusCompleted = "completed"
	ShardStatusFailed    = "failed"
	ShardStatusCancelled = "cancelled"
)

const (
	ErrorInvalidRequest      = "invalid_request"
	ErrorInvalidEndpoint     = "invalid_endpoint"
	ErrorInvalidTransition   = "invalid_transition"
	ErrorDuplicateCustomID   = "duplicate_custom_id"
	ErrorUnknownOutcome      = "unknown_outcome"
	ErrorUnsupportedMode     = "unsupported_execution_mode"
	ErrorIdempotencyConflict = "idempotency_conflict"
	ErrorBatchNotFound       = "batch_not_found"
	ErrorFileNotFound        = "file_not_found"
	ErrorExpired             = "expired"
)

// FileObject is the OpenAI-compatible file resource used for JSONL input and
// output. Content is intentionally outside the DTO and is streamed by the
// Files handler.
type FileObject struct {
	ID            string  `json:"id"`
	Object        string  `json:"object"`
	Bytes         int64   `json:"bytes"`
	CreatedAt     int64   `json:"created_at"`
	Filename      string  `json:"filename"`
	Purpose       string  `json:"purpose"`
	Status        string  `json:"status"`
	StatusDetails *string `json:"status_details"`
}

type FileDeleteResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Deleted bool   `json:"deleted"`
}

type FileList struct {
	Object  string       `json:"object"`
	Data    []FileObject `json:"data"`
	FirstID *string      `json:"first_id"`
	LastID  *string      `json:"last_id"`
	HasMore bool         `json:"has_more"`
}

type BatchList struct {
	Object  string  `json:"object"`
	Data    []Batch `json:"data"`
	FirstID *string `json:"first_id"`
	LastID  *string `json:"last_id"`
	HasMore bool    `json:"has_more"`
}

// BatchError is used both by the standard errors field and item error JSONL.
type BatchError struct {
	Code    string  `json:"code"`
	Message string  `json:"message"`
	Param   *string `json:"param"`
	Line    *int    `json:"line"`
}

// BatchErrorList is the OpenAI list envelope used by Batch.errors.
type BatchErrorList struct {
	Object string       `json:"object"`
	Data   []BatchError `json:"data"`
}

// RequestLine is one JSONL input line. Body is retained verbatim so the
// execution layer can pass the canonical request through protocolrouter.
type RequestLine struct {
	CustomID string          `json:"custom_id"`
	Method   string          `json:"method"`
	URL      string          `json:"url"`
	Body     json.RawMessage `json:"body"`
}

type BatchSubmitRequest struct {
	InputFileID      string            `json:"input_file_id"`
	Endpoint         string            `json:"endpoint"`
	CompletionWindow string            `json:"completion_window"`
	Metadata         map[string]string `json:"metadata,omitempty"`
}

type RequestCounts struct {
	Total     int `json:"total"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
}

// Batch is the public OpenAI-compatible Batch object. Timestamps are Unix
// seconds, matching OpenAI's wire contract; absent timestamps are null.
type Batch struct {
	ID               string            `json:"id"`
	Object           string            `json:"object"`
	Endpoint         string            `json:"endpoint"`
	CompletionWindow string            `json:"completion_window"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Status           string            `json:"status"`
	InputFileID      string            `json:"input_file_id"`
	OutputFileID     *string           `json:"output_file_id"`
	ErrorFileID      *string           `json:"error_file_id"`
	Errors           *BatchErrorList   `json:"errors"`
	RequestCounts    RequestCounts     `json:"request_counts"`
	CreatedAt        int64             `json:"created_at"`
	InProgressAt     *int64            `json:"in_progress_at"`
	FinalizingAt     *int64            `json:"finalizing_at"`
	CancellingAt     *int64            `json:"cancelling_at"`
	CompletedAt      *int64            `json:"completed_at"`
	CancelledAt      *int64            `json:"cancelled_at"`
	ExpiredAt        *int64            `json:"expired_at"`
	FailedAt         *int64            `json:"failed_at"`
	ExpiresAt        *int64            `json:"expires_at"`
}

type ItemResponse struct {
	StatusCode int             `json:"status_code"`
	RequestID  string          `json:"request_id,omitempty"`
	Body       json.RawMessage `json:"body"`
}

// ResultLine is one output or error JSONL row. Exactly one of Response and
// Error is normally present; keeping both pointers permits strict decoding of
// provider output before the indexer applies that invariant.
type ResultLine struct {
	ID       string        `json:"id"`
	CustomID string        `json:"custom_id"`
	Response *ItemResponse `json:"response,omitempty"`
	Error    *BatchError   `json:"error,omitempty"`
}

type BatchItem struct {
	ID           string          `json:"id"`
	BatchID      string          `json:"batch_id"`
	CustomID     string          `json:"custom_id"`
	Method       string          `json:"method"`
	URL          string          `json:"url"`
	Body         json.RawMessage `json:"body"`
	Status       string          `json:"status"`
	AttemptCount int             `json:"attempt_count"`
	Response     *ItemResponse   `json:"response"`
	Error        *BatchError     `json:"error"`
}

type BatchShard struct {
	BatchID   string `json:"batch_id"`
	ShardID   int    `json:"shard_id"`
	Status    string `json:"status"`
	ItemCount int    `json:"item_count"`
	Completed int    `json:"completed"`
	Failed    int    `json:"failed"`
	Version   int64  `json:"version"`
}

// Capability is a provider capability snapshot captured before execution.
// NativeBatch and Managed are independent: a provider may support either or
// both, while the selected execution mode is frozen by the caller.
type Capability struct {
	Provider               string   `json:"provider"`
	Endpoints              []string `json:"endpoints"`
	NativeBatch            bool     `json:"native_batch"`
	Managed                bool     `json:"managed"`
	SupportsIdempotencyKey bool     `json:"supports_idempotency_key"`
}

// BatchCapability is the explicit name used by adapter implementations. The
// alias keeps the shared snapshot type singular while matching the design
// document's public vocabulary.
type BatchCapability = Capability

func (c Capability) SupportsEndpoint(endpoint string) bool {
	for _, candidate := range c.Endpoints {
		if candidate == endpoint {
			return true
		}
	}
	return false
}

// ContractError is suitable for mapping to the OpenAI error envelope.
type ContractError struct{ Code, Message string }

func (e *ContractError) Error() string   { return e.Code + ": " + e.Message }
func invalid(code, message string) error { return &ContractError{Code: code, Message: message} }

func IsEndpointAllowed(endpoint string) bool {
	return endpoint == EndpointChatCompletions || endpoint == EndpointResponses
}

func ValidateSubmit(req BatchSubmitRequest) error {
	if strings.TrimSpace(req.InputFileID) == "" {
		return invalid(ErrorInvalidRequest, "input_file_id is required")
	}
	if !IsEndpointAllowed(req.Endpoint) {
		return invalid(ErrorInvalidEndpoint, "endpoint is not supported")
	}
	if req.CompletionWindow != "24h" {
		return invalid(ErrorInvalidRequest, "completion_window must be 24h")
	}
	if len(req.Metadata) > 16 {
		return invalid(ErrorInvalidRequest, "metadata has more than 16 keys")
	}
	for key, value := range req.Metadata {
		if len(key) > 64 || len(value) > 512 {
			return invalid(ErrorInvalidRequest, "metadata key or value is too long")
		}
	}
	return nil
}

func ValidateRequestLines(lines []RequestLine, endpoint string) error {
	if !IsEndpointAllowed(endpoint) {
		return invalid(ErrorInvalidEndpoint, "endpoint is not supported")
	}
	if len(lines) == 0 {
		return invalid(ErrorInvalidRequest, "input file must contain at least one request")
	}
	seen := make(map[string]struct{}, len(lines))
	for i, line := range lines {
		if strings.TrimSpace(line.CustomID) == "" {
			return invalid(ErrorInvalidRequest, fmt.Sprintf("line %d has empty custom_id", i+1))
		}
		if _, ok := seen[line.CustomID]; ok {
			return invalid(ErrorDuplicateCustomID, "custom_id must be unique")
		}
		seen[line.CustomID] = struct{}{}
		if line.Method != "POST" || line.URL != endpoint {
			return invalid(ErrorInvalidEndpoint, fmt.Sprintf("line %d does not match batch endpoint", i+1))
		}
		body := bytes.TrimSpace(line.Body)
		if len(body) == 0 || body[0] != '{' || !json.Valid(body) || bytes.Equal(body, []byte("null")) {
			return invalid(ErrorInvalidRequest, fmt.Sprintf("line %d has invalid body", i+1))
		}
	}
	return nil
}
