package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
	"github.com/tidwall/gjson"
)

var ErrUniversalCapacityUnavailable = errors.New("universal key: entitled pools are temporarily unavailable")

// UniversalCapacityError wraps ErrUniversalCapacityUnavailable with the best
// known platform when entitled pools support the model but none are currently
// schedulable. Ops uses Platform for attribution when no backing group was bound.
type UniversalCapacityError struct {
	Platform string
	GroupID  int64
}

func (e *UniversalCapacityError) Error() string {
	if e == nil {
		return ErrUniversalCapacityUnavailable.Error()
	}
	return ErrUniversalCapacityUnavailable.Error()
}

func (e *UniversalCapacityError) Unwrap() error { return ErrUniversalCapacityUnavailable }

func newUniversalCapacityError(platform string, groupID int64) error {
	platform = strings.TrimSpace(platform)
	if platform == "" && groupID <= 0 {
		return ErrUniversalCapacityUnavailable
	}
	return &UniversalCapacityError{Platform: platform, GroupID: groupID}
}

func (r *UniversalRoutingResolver) SetCandidateEvaluator(router *protocolrouter.Router, evaluate groupCandidateEvaluator) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.router = router
	r.candidateEvaluator = evaluate
}

// WithRequest uses the same canonical parser as execution, before billing binds
// a group. Malformed/non-text requests retain their handler's validation path.
func (r *UniversalRoutingResolver) WithRequest(ctx context.Context, shape UniversalShape, path, model string, body []byte) context.Context {
	if r == nil {
		return ctx
	}
	r.mu.RLock()
	router := r.router
	r.mu.RUnlock()
	if router == nil || antigravity.IsImageModel(model) {
		return ctx
	}
	var inbound protocolrouter.Protocol
	responsesPath := protocolrouter.ResponsesPathNone
	switch shape {
	case ShapeAnthropicMessages:
		inbound = protocolrouter.ProtocolMessages
	case ShapeAnthropicCountTokens:
		return WithThinkingEnabled(ctx, gatewayRequestThinkingEnabled(body, string(protocolrouter.ProtocolMessages)), false)
	case ShapeOpenAIChat:
		inbound = protocolrouter.ProtocolChatCompletions
		if strings.Contains(path, "/responses") {
			inbound = protocolrouter.ProtocolResponses
			responsesPath = protocolrouter.ResponsesPathRoot
			if strings.HasSuffix(path, "/compact") {
				responsesPath = protocolrouter.ResponsesPathCompact
			}
			if strings.HasSuffix(path, "/input_tokens") {
				responsesPath = protocolrouter.ResponsesPathInputTokens
			}
		}
	case ShapeGemini:
		if !strings.Contains(path, "generateContent") && !strings.Contains(path, "streamGenerateContent") {
			return ctx
		}
		inbound = protocolrouter.ProtocolGeminiGenerateContent
	default:
		return context.WithValue(ctx, protocolRoutingContextKey{}, false)
	}
	// Only stream/type are needed here. Full encoding/json Unmarshal walks the
	// entire payload and dominated live CPU under candidate selection. Match the
	// old Unmarshal-into-struct fail-closed rules for mistyped fields.
	streamRes := gjson.GetBytes(body, "stream")
	if streamRes.Exists() && streamRes.Type != gjson.True && streamRes.Type != gjson.False {
		return ctx
	}
	typeRes := gjson.GetBytes(body, "type")
	if typeRes.Exists() && typeRes.Type != gjson.String {
		return ctx
	}
	stream := streamRes.Bool() || typeRes.String() == "response.create" || strings.Contains(path, ":streamGenerateContent")
	request, err := protocolrouter.ParseCanonicalRequest(inbound, responsesPath, model, stream, body)
	if err != nil {
		return ctx
	}
	if shape != ShapeGemini {
		ctx = WithThinkingEnabled(ctx, gatewayRequestThinkingEnabled(body, string(inbound)), false)
	}
	return WithProtocolRouting(ctx, router, request)
}

func (r *UniversalRoutingResolver) pickCandidateBackingGroup(ctx context.Context, userID int64, eligible []Group, model string, shape UniversalShape, evaluate groupCandidateEvaluator) (*Group, error) {
	type candidate struct {
		group Group
		state GroupCandidateEligibility
	}
	var best *candidate
	var evaluationErr error
	supported := false
	unsupportedModel := false
	var capacityHint *Group
	for _, group := range eligible {
		if universalShapeRequiresImageGenerationEnabled(shape) && !group.AllowImageGeneration {
			continue
		}
		usable, err := r.subscriptionGroupUsable(ctx, userID, &group)
		if err != nil {
			evaluationErr = err
			continue
		}
		if !usable {
			continue
		}
		state, err := evaluate(ctx, group, model, shape)
		if err != nil {
			if errors.Is(err, ErrUniversalUnsupportedModel) {
				unsupportedModel = true
				continue
			}
			if evaluationErr == nil {
				evaluationErr = err
			}
			slog.WarnContext(ctx, "universal_routing.candidate_evaluation_failed", "user_id", userID, "group_id", group.ID, "error", err)
			continue
		}
		supported = supported || state.Supported
		if state.Supported {
			if capacityHint == nil || lessUniversalBacking(group, *capacityHint) {
				g := group
				capacityHint = &g
			}
		}
		if !state.Supported || !state.Available {
			continue
		}
		current := candidate{group, state}
		if best == nil {
			best = &current
			continue
		}
		// Subscription/billing priority precedes soft health preference. Saturation
		// only changes order within a billing tier and never excludes last resorts.
		if group.IsSubscriptionType() != best.group.IsSubscriptionType() {
			if lessUniversalBacking(group, best.group) {
				best = &current
			}
		} else if state.Saturated != best.state.Saturated {
			if !state.Saturated {
				best = &current
			}
		} else if lessUniversalBacking(group, best.group) {
			best = &current
		}
	}
	if best != nil {
		return &best.group, nil
	}
	if evaluationErr != nil && (!supported || !errors.Is(evaluationErr, protocolrouter.ErrNoLegalRoute)) {
		// Prefer a concrete model miss over unknown/conflicted peer capability
		// evidence so Gemini→Chat assembly is not owned as a platform 500 when
		// another peer already classified an unsupported model.
		if !supported && unsupportedModel && (errors.Is(evaluationErr, ErrProtocolCapabilityUnknown) || errors.Is(evaluationErr, ErrProtocolRouteUnavailable)) {
			return nil, fmt.Errorf("%w: %s", ErrUniversalUnsupportedModel, model)
		}
		return nil, evaluationErr
	}
	if supported {
		if capacityHint != nil {
			return nil, newUniversalCapacityError(capacityHint.Platform, capacityHint.ID)
		}
		return nil, ErrUniversalCapacityUnavailable
	}
	if unsupportedModel {
		// A model-bearing request with no supported candidate is a client model
		// miss, not an internal resolver failure. Candidate-local protocol errors
		// have already been filtered by evaluateGroupCandidates.
		return nil, fmt.Errorf("%w: %s", ErrUniversalUnsupportedModel, model)
	}
	return nil, ErrUniversalNoEntitledGroup
}
