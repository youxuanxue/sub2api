package domain

import (
	"encoding/json"
	"testing"
)

func TestGroupModelRoutingUnmarshalObject(t *testing.T) {
	t.Parallel()
	var m GroupModelRouting
	if err := json.Unmarshal([]byte(`{"claude-*":[1,2]}`), &m); err != nil {
		t.Fatal(err)
	}
	if len(m["claude-*"]) != 2 || m["claude-*"][0] != 1 {
		t.Fatalf("unexpected routing: %#v", m)
	}
}

func TestGroupModelRoutingUnmarshalEmptyArrayAsObject(t *testing.T) {
	t.Parallel()
	var m GroupModelRouting
	if err := json.Unmarshal([]byte(`[]`), &m); err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatal("expected non-nil empty map after array coerce")
	}
	if len(m) != 0 {
		t.Fatalf("expected empty map, got %#v", m)
	}
}

func TestGroupModelRoutingUnmarshalNull(t *testing.T) {
	t.Parallel()
	var m GroupModelRouting
	if err := json.Unmarshal([]byte(`null`), &m); err != nil {
		t.Fatal(err)
	}
	if m != nil {
		t.Fatalf("expected nil, got %#v", m)
	}
}

func TestGroupModelRoutingMarshalNeverArray(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(GroupModelRouting{})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != "{}" {
		t.Fatalf("got %s want {}", raw)
	}
}

func TestParseGroupModelRoutingJSONRejectsArray(t *testing.T) {
	t.Parallel()
	_, err := ParseGroupModelRoutingJSON(json.RawMessage(`[]`))
	if err == nil {
		t.Fatal("expected error for array")
	}
}

func TestParseGroupModelRoutingJSONAcceptsObject(t *testing.T) {
	t.Parallel()
	got, err := ParseGroupModelRoutingJSON(json.RawMessage(`{"x":[9]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got["x"][0] != 9 {
		t.Fatalf("unexpected %#v", got)
	}
}

func TestNormalizeGroupModelRouting(t *testing.T) {
	t.Parallel()
	if got := NormalizeGroupModelRouting(true, nil); got == nil || len(got) != 0 {
		t.Fatalf("enabled+nil => empty map, got %#v", got)
	}
	if got := NormalizeGroupModelRouting(false, nil); got != nil {
		t.Fatalf("disabled+nil => nil, got %#v", got)
	}
}
