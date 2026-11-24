package service

import (
	"github.com/Wei-Shaw/sub2api/internal/batchadapter"
	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
)

// The worker has only these two execution bindings. Decide on the actual
// scheduled account and immutable plan, never the billing group's platform.
func managedBatchPlanProvider(selection *AccountSelectionResult) string {
	p, ok := ProtocolPlanFromSelection(selection)
	if !ok || selection.Account == nil || selection.Account.ID != p.AccountID() || p.InboundProtocol() != protocolrouter.ProtocolResponses {
		return ""
	}
	switch p.AdapterID() {
	case protocolrouter.AdapterResponsesIdentity, protocolrouter.AdapterResponsesToChat:
	default:
		return ""
	}
	switch selection.Account.Platform {
	case PlatformOpenAI:
		return batchadapter.ProviderOpenAI
	case PlatformNewAPI:
		return batchadapter.ProviderNewAPI
	default:
		return ""
	}
}
