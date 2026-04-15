package handler

import "strings"

// tkNormalizeTokenKeyInboundAliases maps additional OpenAI-compat paths used by TokenKey routing.
func tkNormalizeTokenKeyInboundAliases(path string) (string, bool) {
	switch {
	case path == "/embeddings":
		return EndpointEmbeddings, true
	case path == "/images/generations":
		return EndpointImagesGenerations, true
	case strings.Contains(path, EndpointImagesGenerations):
		return EndpointImagesGenerations, true
	case strings.Contains(path, EndpointEmbeddings):
		return EndpointEmbeddings, true
	default:
		return "", false
	}
}

// tkDeriveOpenAITokenKeyUpstream maps normalized inbound paths to upstream paths for OpenAI platform accounts.
func tkDeriveOpenAITokenKeyUpstream(inbound string) (string, bool) {
	switch inbound {
	case EndpointEmbeddings:
		return EndpointEmbeddings, true
	case EndpointImagesGenerations:
		return EndpointImagesGenerations, true
	default:
		return "", false
	}
}
