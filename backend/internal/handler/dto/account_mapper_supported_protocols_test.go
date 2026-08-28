package dto

import (
	"reflect"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/engine/protocolrouter"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestAccountFromServiceProjectsCanonicalSupportedProtocols(t *testing.T) {
	lastProbedAt := time.Date(2026, time.August, 27, 0, 0, 0, 0, time.UTC)
	capabilityID := int64(9)
	account := &service.Account{
		ProtocolEndpointCapabilityID: &capabilityID,
		ProtocolEndpointCapability: &service.ProtocolEndpointCapability{
			ID:                 capabilityID,
			CapabilityKey:      "endpoint-capability-key",
			SupportedProtocols: []protocolrouter.Protocol{protocolrouter.ProtocolResponses, protocolrouter.ProtocolMessages, protocolrouter.ProtocolResponses},
			Revision:           7,
			LastProbedAt:       &lastProbedAt,
			LinkedAccountCount: 3,
		},
	}
	out := AccountFromServiceShallow(account)
	got := out.SupportedProtocols
	want := []string{"messages", "responses"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("supported_protocols = %v, want %v", got, want)
	}
	if out.ProtocolCapability == nil {
		t.Fatal("protocol_capability is nil")
	}
	if out.ProtocolCapability.CapabilityKey != "endpoint-capability-key" || out.ProtocolCapability.Revision != 7 {
		t.Fatalf("protocol_capability = %+v", out.ProtocolCapability)
	}
	if out.ProtocolCapability.LastProbedAt == nil || !out.ProtocolCapability.LastProbedAt.Equal(lastProbedAt) {
		t.Fatalf("last_probed_at = %v, want %v", out.ProtocolCapability.LastProbedAt, lastProbedAt)
	}
	if out.ProtocolCapability.AffectedAccountCount != 3 {
		t.Fatalf("affected_account_count = %d, want 3", out.ProtocolCapability.AffectedAccountCount)
	}

	empty := AccountFromServiceShallow(&service.Account{}).SupportedProtocols
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty supported_protocols = %#v, want non-nil empty slice", empty)
	}
}
