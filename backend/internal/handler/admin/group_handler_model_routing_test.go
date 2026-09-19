package admin

import (
	"encoding/json"
	"testing"
)

func TestModelRoutingPayloadRejectsArray(t *testing.T) {
	t.Parallel()
	var m modelRoutingPayload
	if err := json.Unmarshal([]byte(`[]`), &m); err == nil {
		t.Fatal("expected array rejection")
	}
}

func TestModelRoutingPayloadAcceptsObject(t *testing.T) {
	t.Parallel()
	var m modelRoutingPayload
	if err := json.Unmarshal([]byte(`{"claude-*":[1]}`), &m); err != nil {
		t.Fatal(err)
	}
	if m["claude-*"][0] != 1 {
		t.Fatalf("unexpected %#v", m)
	}
	if got := m.asMap()["claude-*"][0]; got != 1 {
		t.Fatalf("asMap mismatch %v", got)
	}
}

func TestModelRoutingPayloadNull(t *testing.T) {
	t.Parallel()
	var m modelRoutingPayload
	if err := json.Unmarshal([]byte(`null`), &m); err != nil {
		t.Fatal(err)
	}
	if m.asMap() != nil {
		t.Fatalf("expected nil map, got %#v", m)
	}
}
