package handler

import "strings"

// tkNormalizeTokenKeyInboundAliases maps additional OpenAI-compat paths used by TokenKey routing.
//
// NOTE: ImagesGenerations / ImagesEdits are also normalized by the upstream-shape
// switch in NormalizeInboundEndpoint; this helper handles the no-`/v1`-prefix
// aliases (e.g. `/embeddings`, `/images/generations`) that upstream does not.
func tkNormalizeTokenKeyInboundAliases(path string) (string, bool) {
	switch {
	case path == "/embeddings":
		return EndpointEmbeddings, true
	case path == "/images/generations":
		return EndpointImagesGenerations, true
	case path == "/images/edits":
		return EndpointImagesEdits, true
	case strings.Contains(path, EndpointEmbeddings):
		return EndpointEmbeddings, true
	default:
		return "", false
	}
}

// tkDeriveOpenAITokenKeyUpstream maps normalized inbound paths to upstream paths
// for OpenAI / NewAPI platform accounts. Endpoints listed here bypass the default
// "everything → /v1/responses" rule and pass through to the matching upstream path.
//
// Covered: Embeddings (TK), ImagesGenerations (upstream + TK), ImagesEdits (upstream).
func tkDeriveOpenAITokenKeyUpstream(inbound string) (string, bool) {
	switch inbound {
	case EndpointEmbeddings:
		return EndpointEmbeddings, true
	case EndpointImagesGenerations:
		return EndpointImagesGenerations, true
	case EndpointImagesEdits:
		return EndpointImagesEdits, true
	default:
		return "", false
	}
}
