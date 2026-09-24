package repository

import (
	"maps"
	"slices"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// cloneLoadedProtocolCapability keeps the independently mutable account results
// produced by the old per-account decoder. Only values decoded by
// protocolCapabilityScanState are cloned here; no snapshot survives a DB read.
func cloneLoadedProtocolCapability(in *service.ProtocolEndpointCapability) *service.ProtocolEndpointCapability {
	out := *in
	out.Identity.ProtocolEndpoints = maps.Clone(in.Identity.ProtocolEndpoints)
	out.Identity.RoutingHeaders = maps.Clone(in.Identity.RoutingHeaders)
	out.SupportedProtocols = slices.Clone(in.SupportedProtocols)
	out.ProbeEvidence.ModelCapabilities = maps.Clone(in.ProbeEvidence.ModelCapabilities)
	for model, protocols := range out.ProbeEvidence.ModelCapabilities {
		out.ProbeEvidence.ModelCapabilities[model] = maps.Clone(protocols)
	}
	out.ProbeEvidence.Verdicts = cloneProtocolEvidenceMap(in.ProbeEvidence.Verdicts)
	if in.LastProbedAt != nil {
		v := *in.LastProbedAt
		out.LastProbedAt = &v
	}
	if in.ProbeLeaseOwner != nil {
		v := *in.ProbeLeaseOwner
		out.ProbeLeaseOwner = &v
	}
	if in.ProbeLeaseUntil != nil {
		v := *in.ProbeLeaseUntil
		out.ProbeLeaseUntil = &v
	}
	return &out
}

// JSON unmarshaled into any contains only maps, slices and immutable scalars.
func cloneProtocolEvidenceJSON(value any) any {
	switch value := value.(type) {
	case map[string]any:
		return cloneProtocolEvidenceMap(value)
	case []any:
		out := make([]any, len(value))
		for i, v := range value {
			out[i] = cloneProtocolEvidenceJSON(v)
		}
		return out
	default:
		return value
	}
}

func cloneProtocolEvidenceMap(value map[string]any) map[string]any {
	if value == nil {
		return nil
	}
	out := make(map[string]any, len(value))
	for k, v := range value {
		out[k] = cloneProtocolEvidenceJSON(v)
	}
	return out
}
