package dto

import (
	"reflect"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

func TestAccountFromServiceProjectsCanonicalSupportedProtocols(t *testing.T) {
	account := &service.Account{Extra: map[string]any{
		service.SupportedProtocolsExtraKey: []any{"responses", "messages", "responses"},
	}}
	got := AccountFromServiceShallow(account).SupportedProtocols
	want := []string{"messages", "responses"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("supported_protocols = %v, want %v", got, want)
	}

	empty := AccountFromServiceShallow(&service.Account{}).SupportedProtocols
	if empty == nil || len(empty) != 0 {
		t.Fatalf("empty supported_protocols = %#v, want non-nil empty slice", empty)
	}
}
