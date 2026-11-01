package batchcontract

import "fmt"

// ExecutionSelection is the frozen decision persisted with a Batch before
// work starts. Capability is copied as a snapshot so later registry changes
// cannot silently change an in-flight Batch.
type ExecutionSelection struct {
	Mode                 string     `json:"execution_mode"`
	Capability           Capability `json:"capability"`
	NativeFallbackReason string     `json:"native_fallback_reason,omitempty"`
}

func IsValidExecutionMode(mode string) bool {
	return mode == ExecutionModePending || mode == ExecutionModeNative || mode == ExecutionModeManaged
}

func ValidateExecutionMode(mode string) error {
	if !IsValidExecutionMode(mode) {
		return invalid(ErrorUnsupportedMode, fmt.Sprintf("invalid execution mode %q", mode))
	}
	return nil
}

var batchTransitions = map[string]map[string]struct{}{
	BatchStatusValidating: {BatchStatusInProgress: {}, BatchStatusFailed: {}, BatchStatusCancelling: {}},
	BatchStatusInProgress: {BatchStatusFinalizing: {}, BatchStatusCancelling: {}},
	BatchStatusCancelling: {BatchStatusFinalizing: {}},
	BatchStatusFinalizing: {BatchStatusCompleted: {}, BatchStatusFailed: {}, BatchStatusCancelled: {}, BatchStatusExpired: {}},
}

var itemTransitions = map[string]map[string]struct{}{
	ItemStatusPending: {ItemStatusRunning: {}, ItemStatusCancelled: {}},
	ItemStatusRunning: {ItemStatusSucceeded: {}, ItemStatusFailed: {}, ItemStatusCancelled: {}, ItemStatusUnknown: {}},
}

var shardTransitions = map[string]map[string]struct{}{
	ShardStatusPending: {ShardStatusRunning: {}, ShardStatusCancelled: {}},
	ShardStatusRunning: {ShardStatusCompleted: {}, ShardStatusFailed: {}, ShardStatusCancelled: {}},
}

func CanTransitionBatch(from, to string) bool { _, ok := batchTransitions[from][to]; return ok }
func CanTransitionItem(from, to string) bool  { _, ok := itemTransitions[from][to]; return ok }
func CanTransitionShard(from, to string) bool { _, ok := shardTransitions[from][to]; return ok }

func TransitionBatch(from, to string) error {
	if !CanTransitionBatch(from, to) {
		return invalid(ErrorInvalidTransition, fmt.Sprintf("batch cannot transition from %q to %q", from, to))
	}
	return nil
}
func TransitionItem(from, to string) error {
	if !CanTransitionItem(from, to) {
		return invalid(ErrorInvalidTransition, fmt.Sprintf("item cannot transition from %q to %q", from, to))
	}
	return nil
}
func TransitionShard(from, to string) error {
	if !CanTransitionShard(from, to) {
		return invalid(ErrorInvalidTransition, fmt.Sprintf("shard cannot transition from %q to %q", from, to))
	}
	return nil
}

func IsBatchTerminal(status string) bool {
	switch status {
	case BatchStatusCompleted, BatchStatusFailed, BatchStatusCancelled, BatchStatusExpired:
		return true
	default:
		return false
	}
}
func IsItemTerminal(status string) bool {
	switch status {
	case ItemStatusSucceeded, ItemStatusFailed, ItemStatusCancelled, ItemStatusUnknown:
		return true
	default:
		return false
	}
}

// CanFallbackToManaged is intentionally strict: an unknown native submission
// or any started item must remain on the same provider and be reconciled.
func CanFallbackToManaged(mode string, nativeUnsupported, providerSubmitted, itemStarted bool, capability Capability) bool {
	return mode == ExecutionModeNative && nativeUnsupported && !providerSubmitted && !itemStarted && capability.Managed
}

func SelectExecutionMode(capability Capability, nativePreferred bool) (string, error) {
	if nativePreferred && capability.NativeBatch {
		return ExecutionModeNative, nil
	}
	if capability.Managed {
		return ExecutionModeManaged, nil
	}
	if capability.NativeBatch {
		return ExecutionModeNative, nil
	}
	return "", invalid(ErrorUnsupportedMode, "capability has no usable execution mode")
}
