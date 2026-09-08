package service

import "strings"

func CompositeRouteEndpointForPath(path string) string {
	switch {
	case strings.Contains(path, "/messages/count_tokens"):
		return CompositeRouteEndpointCountTokens
	case strings.Contains(path, "/messages"):
		return CompositeRouteEndpointMessages
	case strings.Contains(path, "/responses"), strings.Contains(path, "/alpha/search"),
		strings.Contains(path, "/realtime/calls"), strings.HasSuffix(strings.TrimRight(path, "/"), "/live"):
		return CompositeRouteEndpointResponses
	case strings.Contains(path, "/chat/completions"):
		return CompositeRouteEndpointChatCompletions
	case strings.Contains(path, "/embeddings"):
		return CompositeRouteEndpointEmbeddings
	case strings.Contains(path, "/images/"):
		return CompositeRouteEndpointImages
	case strings.Contains(path, "/v1beta/"):
		return CompositeRouteEndpointGemini
	default:
		return CompositeRouteEndpointAny
	}
}
