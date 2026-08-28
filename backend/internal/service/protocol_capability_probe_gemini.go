package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

func classifyGeminiProtocolProbe(status int, body []byte, requestErr error) ProtocolProbeVerdict {
	if requestErr != nil {
		return ProtocolProbeInconclusive
	}
	if status >= http.StatusOK && status < http.StatusMultipleChoices {
		if geminiProtocolProbeResponseParseable(body) {
			return ProtocolProbePositive
		}
		return ProtocolProbeInconclusive
	}
	if (status == http.StatusNotFound || status == http.StatusMethodNotAllowed) &&
		(geminiProtocolProbeHasReason(body, "METHOD_NOT_SUPPORTED") ||
			geminiProtocolProbeHasReason(body, "UNSUPPORTED_METHOD")) {
		return ProtocolProbeEndpointNegative
	}
	lower := strings.ToLower(string(body))
	if status == http.StatusNotFound &&
		(strings.Contains(lower, "model") || strings.Contains(lower, "location") ||
			strings.Contains(lower, "project") || strings.Contains(lower, "publisher")) {
		return ProtocolProbeModelSpecific
	}
	return ProtocolProbeInconclusive
}

func geminiProtocolProbeResponseParseable(body []byte) bool {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return false
	}
	var document map[string]any
	if json.Unmarshal(body, &document) == nil {
		return geminiProtocolProbeDocumentHasResponse(document)
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		payload := bytes.TrimSpace(bytes.TrimPrefix(line, []byte("data:")))
		if bytes.Equal(payload, []byte("[DONE]")) {
			continue
		}
		if json.Unmarshal(payload, &document) == nil && geminiProtocolProbeDocumentHasResponse(document) {
			return true
		}
	}
	return false
}

func geminiProtocolProbeDocumentHasResponse(document map[string]any) bool {
	if len(document) == 0 {
		return false
	}
	if response, ok := document["response"].(map[string]any); ok {
		return geminiProtocolProbeDocumentHasResponse(response)
	}
	if candidates, ok := document["candidates"].([]any); ok {
		return candidates != nil
	}
	if feedback, ok := document["promptFeedback"].(map[string]any); ok {
		return len(feedback) > 0
	}
	if usage, ok := document["usageMetadata"].(map[string]any); ok {
		return len(usage) > 0
	}
	if modelVersion, ok := document["modelVersion"].(string); ok && strings.TrimSpace(modelVersion) != "" {
		return true
	}
	if responseID, ok := document["responseId"].(string); ok && strings.TrimSpace(responseID) != "" {
		return true
	}
	return false
}

func geminiProtocolProbeHasReason(body []byte, wanted string) bool {
	var document any
	if json.Unmarshal(body, &document) != nil {
		return false
	}
	wanted = strings.ToUpper(strings.TrimSpace(wanted))
	var visit func(any) bool
	visit = func(value any) bool {
		switch current := value.(type) {
		case []any:
			for _, item := range current {
				if visit(item) {
					return true
				}
			}
		case map[string]any:
			for key, child := range current {
				if strings.EqualFold(key, "reason") {
					if reason, ok := child.(string); ok && strings.ToUpper(strings.TrimSpace(reason)) == wanted {
						return true
					}
				}
				if visit(child) {
					return true
				}
			}
		}
		return false
	}
	return visit(document)
}

func classifyAntigravityGeminiProtocolProbe(result *TestConnectionResult, err error) ProtocolProbeVerdict {
	if result != nil && result.StatusCode != 0 {
		return classifyGeminiProtocolProbe(result.StatusCode, result.ResponseBody, nil)
	}
	var failover *UpstreamFailoverError
	if errors.As(err, &failover) {
		return classifyGeminiProtocolProbe(failover.StatusCode, failover.ResponseBody, nil)
	}
	return ProtocolProbeInconclusive
}

func (s *AccountTestService) probeGeminiGenerateContentSupport(
	ctx context.Context,
	account *Account,
) (protocolProbeObservation, bool) {
	observation := protocolProbeObservation{protocol: protocolrouter.ProtocolGeminiGenerateContent}
	profile := protocolGeminiEndpointProfile(account)
	if !profile.Valid() {
		return observation, false
	}
	model := strings.TrimSpace(selectProtocolProbeModel(account))
	if model == "" {
		model = AntigravityDefaultTestModelID
	}

	switch profile {
	case protocolrouter.GeminiEndpointAntigravityCloudCode:
		if s == nil || s.antigravityGatewayService == nil {
			observation.verdict = ProtocolProbeInconclusive
			return observation, true
		}
		result, err := s.antigravityGatewayService.TestConnection(ctx, account, model)
		observation.verdict = classifyAntigravityGeminiProtocolProbe(result, err)
		return observation, true

	case protocolrouter.GeminiEndpointVertexServiceAccount:
		if s == nil || s.httpUpstream == nil {
			observation.verdict = ProtocolProbeInconclusive
			return observation, true
		}
		payload := createGeminiTestPayload(model, defaultGeminiTextTestPrompt)
		req, err := s.buildGeminiServiceAccountRequest(ctx, account, model, payload)
		if err != nil {
			observation.verdict = ProtocolProbeInconclusive
			return observation, true
		}
		proxyURL := ""
		if account.ProxyID != nil && account.Proxy != nil {
			proxyURL = account.Proxy.URL()
		}
		resp, err := s.httpUpstream.DoWithTLS(req, proxyURL, account.ID, account.Concurrency, s.resolveAccountTestTLSProfile(account))
		if err != nil {
			observation.verdict = classifyGeminiProtocolProbe(0, nil, err)
			return observation, true
		}
		if resp == nil {
			observation.verdict = ProtocolProbeInconclusive
			return observation, true
		}
		defer func() { _ = resp.Body.Close() }()
		body, readErr := io.ReadAll(resp.Body)
		observation.verdict = classifyGeminiProtocolProbe(resp.StatusCode, body, readErr)
		return observation, true
	}
	return observation, false
}
