package service

import (
	"context"
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

// accountAdmitsRequestInputModalities intersects request ContentKinds with the
// evidence-backed supply known-negative table. Unknown supplies stay eligible.
// Owner: docs/approved/multimodal-supply-capability-ssot.md
func accountAdmitsRequestInputModalities(ctx context.Context, account *Account, model string) bool {
	if account == nil {
		return false
	}
	req, ok := ProtocolRoutingRequest(ctx)
	if !ok {
		return true
	}
	kinds := req.Profile().ContentKinds
	if kinds&protocolrouter.ContentVideo == 0 {
		return true
	}
	return !supplyKnownNegativeForInputVideo(account, model)
}

// supplyKnownNegativeForInputVideo reports evidence-backed "this supply cannot
// consume input video for this client model". Absence from the table is not a
// negative — callers must keep unknown supplies eligible.
func supplyKnownNegativeForInputVideo(account *Account, model string) bool {
	normalized := normalizeMultimodalSupplyModelID(model)
	if normalized == "" {
		return false
	}
	switch normalized {
	case "kimi-k3":
		// Probe 2026-10-08: NVIDIA Build rejects video_url ("At most 0 video(s)");
		// Volc Agent Plan /api/plan/v3 responses servable with video.
		return isNewAPINVIDIABuildAccount(account)
	default:
		return false
	}
}

func normalizeMultimodalSupplyModelID(model string) string {
	model = strings.ToLower(strings.TrimSpace(model))
	if i := strings.IndexByte(model, '['); i > 0 {
		model = model[:i]
	}
	return strings.TrimSpace(model)
}
