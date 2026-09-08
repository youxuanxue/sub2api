package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

var ErrUniversalCapacityUnavailable = errors.New("universal key: entitled pools are temporarily unavailable")

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
		return ctx
	}
	var flags struct {
		Stream bool   `json:"stream"`
		Type   string `json:"type"`
	}
	if json.Unmarshal(body, &flags) != nil {
		return ctx
	}
	stream := flags.Stream || flags.Type == "response.create" || strings.Contains(path, ":streamGenerateContent")
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
		return nil, evaluationErr
	}
	if supported {
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
