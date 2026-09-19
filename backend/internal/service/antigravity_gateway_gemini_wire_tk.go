package service

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/pkg/antigravity"
)

// normalizeForwardGeminiGenerateContentBody applies the ForwardGemini generateContent
// prep steps (identity patch, roles, schema clean, mixed-tool reconcile) without
// wrapping. Callers wrap via prepareForwardGeminiWire / prepareForwardGeminiWireBody.
func (s *AntigravityGatewayService) normalizeForwardGeminiGenerateContentBody(generateContentBody []byte) ([]byte, error) {
	injectedBody, err := injectIdentityPatchToGeminiRequest(generateContentBody)
	if err != nil {
		return nil, err
	}
	injectedBody = tkEnsureGeminiContentRoles(injectedBody)
	if cleanedBody, cleanErr := cleanGeminiRequest(injectedBody); cleanErr == nil {
		injectedBody = cleanedBody
	}
	if reconciled, reconcileErr := enableMixedGeminiToolInvocations(injectedBody); reconcileErr == nil {
		injectedBody = reconciled
	}
	return injectedBody, nil
}

// prepareForwardGeminiWire normalizes then wraps the native v1internal envelope.
// ForwardGemini keeps the normalized generateContent body for model-fallback /
// signature-rectify retries; messages/chat/responses only need the wrapped bytes.
func (s *AntigravityGatewayService) prepareForwardGeminiWire(projectID, mappedModel string, generateContentBody []byte) (normalized, wrapped []byte, err error) {
	normalized, err = s.normalizeForwardGeminiGenerateContentBody(generateContentBody)
	if err != nil {
		return nil, nil, err
	}
	wrapped, err = s.wrapV1InternalRequest(projectID, mappedModel, normalized)
	if err != nil {
		return nil, nil, err
	}
	return normalized, wrapped, nil
}

// prepareForwardGeminiWireBody is the shared AG Gemini upstream-wire owner for
// converters that land on gemini_generate_content (messages/chat/responses).
// It returns only the wrapped v1internal body (requestType / enabledCreditTypes included).
func (s *AntigravityGatewayService) prepareForwardGeminiWireBody(projectID, mappedModel string, generateContentBody []byte) ([]byte, error) {
	_, wrapped, err := s.prepareForwardGeminiWire(projectID, mappedModel, generateContentBody)
	return wrapped, err
}

// isAntigravityGeminiFamilyModel reports mapped models that must use the
// ForwardGemini wire (generateContent → v1internal), not Claude TransformClaudeToGemini.
func isAntigravityGeminiFamilyModel(mappedModel string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(mappedModel)), "gemini-")
}

// buildMessagesForwardUpstreamBody routes Messages-ingress AG traffic:
// gemini-* → converter → ForwardGemini wire; Claude-family → TransformClaudeToGemini.
func (s *AntigravityGatewayService) buildMessagesForwardUpstreamBody(
	ctx context.Context,
	claudeBody []byte,
	claudeReq *antigravity.ClaudeRequest,
	projectID, mappedModel string,
	transformOpts antigravity.TransformOptions,
) ([]byte, error) {
	if isAntigravityGeminiFamilyModel(mappedModel) {
		if len(claudeBody) == 0 && claudeReq != nil {
			var err error
			claudeBody, err = json.Marshal(claudeReq)
			if err != nil {
				return nil, err
			}
		}
		return s.buildAntigravityCompatGeminiBody(ctx, claudeBody, claudeReq, projectID, mappedModel)
	}
	return antigravity.TransformClaudeToGeminiWithOptions(claudeReq, projectID, mappedModel, transformOpts)
}
