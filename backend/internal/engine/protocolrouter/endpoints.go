package protocolrouter

import (
	"errors"
	"fmt"
	"net/url"
	"path"
	"strings"
)

var officialEndpointBases = map[OfficialEndpointProfile]string{
	OfficialEndpointAnthropic: "https://api.anthropic.com",
	OfficialEndpointOpenAI:    "https://api.openai.com",
}

func resolveEndpoint(account AccountSnapshot, target Protocol, responsesPath ResponsesPathKind) (string, error) {
	if endpoint := account.exactEndpoints[target]; endpoint != "" {
		return validateExactEndpoint(endpoint)
	}
	if account.officialProfile == OfficialEndpointOpenAICodex {
		if target != ProtocolResponses {
			return "", errors.New("OpenAI Codex official profile supports responses only")
		}
		switch responsesPath {
		case ResponsesPathNone, ResponsesPathRoot:
			return "https://chatgpt.com/backend-api/codex/responses", nil
		case ResponsesPathCompact:
			return "https://chatgpt.com/backend-api/codex/responses/compact", nil
		case ResponsesPathInputTokens:
			// Codex has no input_tokens endpoint. The root endpoint is only the
			// validated account-route anchor; the token-count executor estimates
			// locally and never sends this request upstream.
			return "https://chatgpt.com/backend-api/codex/responses", nil
		default:
			return "", fmt.Errorf("OpenAI Codex official profile does not support responses path %q", responsesPath)
		}
	}
	baseURL := account.customBaseURLs[target]
	if baseURL == "" {
		baseURL = account.customBaseURL
	}
	if account.officialProfile != "" {
		var ok bool
		baseURL, ok = officialEndpointBases[account.officialProfile]
		if !ok {
			return "", fmt.Errorf("unregistered official endpoint profile %q", account.officialProfile)
		}
	}
	if strings.TrimSpace(baseURL) == "" {
		return "", errors.New("explicit endpoint is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("endpoint scheme must be http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("endpoint must contain only scheme, host, and path")
	}
	endpointPath, err := protocolEndpointPath(target, responsesPath)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimSuffix(parsed.Path, "/")
	if strings.HasSuffix(basePath, "/v1") && strings.HasPrefix(endpointPath, "/v1/") {
		endpointPath = strings.TrimPrefix(endpointPath, "/v1")
	}
	parsed.Path = path.Clean(basePath + endpointPath)
	if !strings.HasPrefix(parsed.Path, "/") {
		parsed.Path = "/" + parsed.Path
	}
	return parsed.String(), nil
}

func validateExactEndpoint(endpoint string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil {
		return "", fmt.Errorf("parse endpoint: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errors.New("endpoint scheme must be http or https")
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("endpoint must contain only scheme, host, and path")
	}
	if strings.TrimSpace(parsed.Path) == "" || parsed.Path == "/" {
		return "", errors.New("exact endpoint path is required")
	}
	return parsed.String(), nil
}

func protocolEndpointPath(target Protocol, responsesPath ResponsesPathKind) (string, error) {
	switch target {
	case ProtocolMessages:
		return "/v1/messages", nil
	case ProtocolChatCompletions:
		return "/v1/chat/completions", nil
	case ProtocolResponses:
		switch responsesPath {
		case ResponsesPathNone, ResponsesPathRoot:
			return "/v1/responses", nil
		case ResponsesPathCompact:
			return "/v1/responses/compact", nil
		case ResponsesPathInputTokens:
			return "/v1/responses/input_tokens", nil
		default:
			return "", fmt.Errorf("unsupported responses path %q", responsesPath)
		}
	default:
		return "", fmt.Errorf("unsupported target protocol %q", target)
	}
}
