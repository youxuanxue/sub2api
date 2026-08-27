package handler

import (
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func attachHandlerTestProtocolCapability(
	t *testing.T,
	account *service.Account,
	protocols ...protocolrouter.Protocol,
) {
	t.Helper()
	identity, governed, err := service.BuildProtocolEndpointIdentity(account)
	if err != nil {
		t.Fatalf("BuildProtocolEndpointIdentity: %v", err)
	}
	if !governed {
		t.Fatalf("account %d is not governed", account.ID)
	}
	capabilityID := account.ID
	if capabilityID <= 0 {
		capabilityID = 1
	}
	account.ProtocolEndpointCapabilityID = &capabilityID
	account.ProtocolEndpointCapability = &service.ProtocolEndpointCapability{
		ID:                 capabilityID,
		CapabilityKey:      identity.Key(),
		Identity:           identity,
		SupportedProtocols: append([]protocolrouter.Protocol(nil), protocols...),
		ProbeEvidence: service.ProtocolProbeEvidence{
			InitialProbeCompleted: true,
		},
		Revision: 1,
	}
}
