// Package batchadapter contains the provider boundary for Managed Batch.
//
// Adapters deliberately do not choose accounts or implement relay transports.
// A runner supplied by the service layer is responsible for invoking the
// existing protocolrouter/relay path for one request. This package only owns
// the Batch contract boundary and provider wire normalization.
package batchadapter

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/batchcontract"
)

const (
	ProviderOpenAI      = "openai"
	ProviderAntigravity = "antigravity"
	ProviderKiro        = "kiro"
)

// BatchCapability is the provider capability snapshot persisted by the batch
// service. It is an alias so callers use the same value as batchcontract.
type BatchCapability = batchcontract.BatchCapability

// ManagedRequest is the single item handed to the existing protocol runner.
// Account selection is intentionally absent: the runner receives the account
// identity selected by the normal gateway scheduler in its own context.
type ManagedRequest struct {
	// ExecutionContext carries the scheduler-created Plan and account scope.
	// It is opaque to adapters; the service runner consumes it to re-enter the
	// normal protocol execution path without re-selecting an account.
	ExecutionContext context.Context
	AccountID        int64
	CustomID         string
	Endpoint         string
	Method           string
	Model            string
	Body             json.RawMessage
}

// ExecutionRequest is the provider-neutral input to the normal account
// scheduler. Keeping it here avoids a dependency from service back into the
// worker package.
type ExecutionRequest struct {
	BatchID   string
	UserID    int64
	APIKeyID  int64
	Provider  string
	AccountID int64
}

// ExecutionBatch and ExecutionItem are the small immutable views needed by a
// scheduler resolver. They keep the service boundary independent of the SQL
// repository package (which itself depends on service types).
type ExecutionBatch struct {
	ID       string
	UserID   int64
	APIKeyID int64
	Endpoint string
}

type ExecutionItem struct {
	ID       int64
	BatchID  string
	CustomID string
}

// ExecutionSelection is returned by the scheduler owner. Release must be
// called exactly once after the provider attempt finishes.
type ExecutionSelection struct {
	Provider  string
	AccountID int64
	Context   context.Context
	Release   func()
}

// ManagedResponse is the normalized single-request result returned to the
// Batch indexer. Usage is kept separate so billing can consume the same
// provider-independent counters as the normal relay path.
type ManagedResponse struct {
	Response                      batchcontract.ItemResponse
	Usage                         Usage
	Error                         *ProviderError
	Model                         string
	UpstreamModel                 string
	BillingModel                  string
	ServiceTier                   *string
	ReasoningEffort               *string
	RequestedReasoningEffort      *string
	UpstreamResponseModel         string
	UpstreamResponseModelConflict bool
	UpstreamEndpoint              string
}

// ManagedRunner is implemented by the service layer with the existing
// protocolrouter/relay execution. Implementations must preserve account and
// usage attribution; adapters never reimplement routing.
type ManagedRunner interface {
	Run(context.Context, ProviderRequest) (ProviderResponse, error)
}

// ManagedExecutor is the narrow execution surface used by a worker.
type ManagedExecutor interface {
	Provider() string
	Capability() BatchCapability
	ValidateRequest(batchcontract.RequestLine, string) error
	Execute(context.Context, ManagedRequest) (ManagedResponse, error)
}

// NativeBatchAdapter is intentionally small. Native providers can be plugged
// in without changing the public Batch contract; until then the managed path
// remains the fallback selected by batchcontract.SelectExecutionMode.
type NativeBatchAdapter interface {
	Provider() string
	Capability() BatchCapability
	Submit(context.Context, NativeSubmitRequest) (NativeBatchJob, error)
	Status(context.Context, NativeBatchJob) (NativeBatchStatus, error)
	Cancel(context.Context, NativeBatchJob) error
	Results(context.Context, NativeBatchJob) ([]batchcontract.ResultLine, error)
}

type NativeSubmitRequest struct {
	InputFileID      string
	Endpoint         string
	CompletionWindow string
}

type NativeBatchJob struct{ ID string }

type NativeBatchStatus struct {
	Status        string
	RequestCounts batchcontract.RequestCounts
}

// ProviderRequest is the wire request produced by the adapter and consumed by
// the injected runner. Endpoint and body are already validated against the
// OpenAI-compatible JSONL line.
type ProviderRequest struct {
	ExecutionContext context.Context
	AccountID        int64
	Endpoint         string
	Method           string
	Model            string
	Body             json.RawMessage
}

// ProviderResponse is the small response shape needed to build an OpenAI
// Batch result line. Raw body remains untouched for result-file fidelity.
type ProviderResponse struct {
	StatusCode                    int
	RequestID                     string
	Body                          json.RawMessage
	Headers                       map[string]string
	Model                         string
	UpstreamModel                 string
	BillingModel                  string
	ServiceTier                   *string
	ReasoningEffort               *string
	RequestedReasoningEffort      *string
	UpstreamResponseModel         string
	UpstreamResponseModelConflict bool
	UpstreamEndpoint              string
}

type Usage struct {
	InputTokens          int `json:"input_tokens"`
	OutputTokens         int `json:"output_tokens"`
	TotalTokens          int `json:"total_tokens"`
	CacheReadInputTokens int `json:"cache_read_input_tokens,omitempty"`
}

type ErrorClass string

const (
	ErrorRetryable ErrorClass = "retryable"
	ErrorPermanent ErrorClass = "permanent"
	ErrorCancelled ErrorClass = "cancelled"
	ErrorUnknown   ErrorClass = "unknown"
)

type ProviderError struct {
	Class      ErrorClass
	StatusCode int
	Code       string
	Message    string
}

func (e *ProviderError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

var (
	ErrInvalidEndpoint   = errors.New("batch adapter: invalid endpoint")
	ErrInvalidRequest    = errors.New("batch adapter: invalid request")
	ErrNativeUnsupported = errors.New("batch adapter: native batch unsupported")
	ErrManagedDisabled   = errors.New("batch adapter: managed execution disabled")
)

// ProviderAdapter implements the shared ManagedExecutor behavior for OAuth
// providers. The per-provider constructors only supply capability metadata and
// usage decoding; account selection and relay remain in ManagedRunner.
type ProviderAdapter struct {
	provider    string
	capability  BatchCapability
	runner      ManagedRunner
	decodeUsage func([]byte) Usage
}

func NewOpenAIAdapter(runner ManagedRunner) *ProviderAdapter {
	return newProviderAdapter(ProviderOpenAI, BatchCapability{
		Provider:    ProviderOpenAI,
		Endpoints:   []string{batchcontract.EndpointChatCompletions, batchcontract.EndpointResponses},
		NativeBatch: false, Managed: true, SupportsIdempotencyKey: true,
	}, runner, decodeOpenAIUsage)
}

func NewAntigravityAdapter(runner ManagedRunner) *ProviderAdapter {
	return newProviderAdapter(ProviderAntigravity, BatchCapability{
		Provider:    ProviderAntigravity,
		Endpoints:   []string{batchcontract.EndpointChatCompletions, batchcontract.EndpointResponses},
		NativeBatch: false, Managed: true, SupportsIdempotencyKey: false,
	}, runner, decodeAntigravityUsage)
}

func NewKiroAdapter(runner ManagedRunner) *ProviderAdapter {
	return newProviderAdapter(ProviderKiro, BatchCapability{
		Provider:    ProviderKiro,
		Endpoints:   []string{batchcontract.EndpointChatCompletions, batchcontract.EndpointResponses},
		NativeBatch: false, Managed: false, SupportsIdempotencyKey: false,
	}, runner, decodeKiroUsage)
}

func newProviderAdapter(provider string, capability BatchCapability, runner ManagedRunner, decode func([]byte) Usage) *ProviderAdapter {
	return &ProviderAdapter{provider: provider, capability: capability, runner: runner, decodeUsage: decode}
}

func (a *ProviderAdapter) Provider() string {
	if a == nil {
		return ""
	}
	return a.provider
}
func (a *ProviderAdapter) Capability() BatchCapability {
	if a == nil {
		return BatchCapability{}
	}
	return a.capability
}

func (a *ProviderAdapter) ValidateRequest(line batchcontract.RequestLine, endpoint string) error {
	if a == nil || !a.capability.SupportsEndpoint(endpoint) || line.URL != endpoint || line.Method != http.MethodPost {
		return fmt.Errorf("%w: %s", ErrInvalidEndpoint, endpoint)
	}
	if err := batchcontract.ValidateRequestLines([]batchcontract.RequestLine{line}, endpoint); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return nil
}

func (a *ProviderAdapter) Execute(ctx context.Context, request ManagedRequest) (ManagedResponse, error) {
	if a == nil || !a.capability.Managed {
		return ManagedResponse{}, ErrManagedDisabled
	}
	if a.runner == nil {
		return ManagedResponse{}, errors.New("batch adapter: managed runner is nil")
	}
	if request.AccountID <= 0 || !a.capability.SupportsEndpoint(request.Endpoint) || request.Method != http.MethodPost || len(request.Body) == 0 {
		return ManagedResponse{}, ErrInvalidRequest
	}
	executionCtx := request.ExecutionContext
	if executionCtx == nil {
		executionCtx = ctx
	}
	response, err := a.runner.Run(ctx, ProviderRequest{ExecutionContext: executionCtx, AccountID: request.AccountID, Endpoint: request.Endpoint, Method: request.Method, Model: request.Model, Body: append([]byte(nil), request.Body...)})
	if err != nil {
		return ManagedResponse{}, err
	}
	result := ManagedResponse{
		Response:                      batchcontract.ItemResponse{StatusCode: response.StatusCode, RequestID: response.RequestID, Body: append([]byte(nil), response.Body...)},
		Model:                         response.Model,
		UpstreamModel:                 response.UpstreamModel,
		BillingModel:                  response.BillingModel,
		ServiceTier:                   response.ServiceTier,
		ReasoningEffort:               response.ReasoningEffort,
		RequestedReasoningEffort:      response.RequestedReasoningEffort,
		UpstreamResponseModel:         response.UpstreamResponseModel,
		UpstreamResponseModelConflict: response.UpstreamResponseModelConflict,
		UpstreamEndpoint:              response.UpstreamEndpoint,
	}
	result.Usage = a.decodeUsage(response.Body)
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		result.Error = classifyHTTPError(response.StatusCode, response.Body)
	}
	return result, nil
}

// UnsupportedNativeAdapter makes capability-gated native behavior explicit.
// It is used by the first managed-only provider rollout and can be replaced by
// a provider implementation without changing ManagedExecutor.
type UnsupportedNativeAdapter struct{ capability BatchCapability }

func NewUnsupportedNativeAdapter(capability BatchCapability) *UnsupportedNativeAdapter {
	return &UnsupportedNativeAdapter{capability: capability}
}
func (a *UnsupportedNativeAdapter) Provider() string {
	if a == nil {
		return ""
	}
	return a.capability.Provider
}
func (a *UnsupportedNativeAdapter) Capability() BatchCapability {
	if a == nil {
		return BatchCapability{}
	}
	return a.capability
}
func (*UnsupportedNativeAdapter) Submit(context.Context, NativeSubmitRequest) (NativeBatchJob, error) {
	return NativeBatchJob{}, ErrNativeUnsupported
}
func (*UnsupportedNativeAdapter) Status(context.Context, NativeBatchJob) (NativeBatchStatus, error) {
	return NativeBatchStatus{}, ErrNativeUnsupported
}
func (*UnsupportedNativeAdapter) Cancel(context.Context, NativeBatchJob) error {
	return ErrNativeUnsupported
}
func (*UnsupportedNativeAdapter) Results(context.Context, NativeBatchJob) ([]batchcontract.ResultLine, error) {
	return nil, ErrNativeUnsupported
}

func classifyHTTPError(status int, body []byte) *ProviderError {
	class := ErrorPermanent
	if status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= 500 {
		class = ErrorRetryable
	}
	if status == 0 {
		class = ErrorUnknown
	}
	var payload struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	code, message := payload.Error.Code, payload.Error.Message
	if code == "" {
		code = payload.Error.Type
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	return &ProviderError{Class: class, StatusCode: status, Code: code, Message: message}
}
