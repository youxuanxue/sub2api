package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"net/url"
	"strings"
)

func protocolPlanEndpoint(endpoint string) string {
	parsed, err := url.Parse(strings.TrimSpace(endpoint))
	if err != nil || parsed.Path == "" {
		return strings.TrimSpace(endpoint)
	}
	return parsed.Path
}

func newCanonicalProtocolRequest(inbound protocolrouter.Protocol, responsesPath protocolrouter.ResponsesPathKind, model string, stream bool, body []byte) (protocolrouter.CanonicalRequest, error) {
	return protocolrouter.ParseCanonicalRequest(inbound, responsesPath, model, stream, body)
}
