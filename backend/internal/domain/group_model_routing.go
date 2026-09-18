package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
)

// GroupModelRouting is groups.model_routing: model pattern → preferred account IDs.
// Ent/JSON must be an object. Probe/SQL bugs historically wrote a JSON array
// (including []), which made every Group Scan fail and took down universal-key
// routing via ListActiveGroups. Tolerate array on read as an empty object.
type GroupModelRouting map[string][]int64

// UnmarshalJSON accepts object/null and remaps array (incl. []) to {}.
func (m *GroupModelRouting) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || bytes.Equal(data, []byte("null")) {
		*m = nil
		return nil
	}
	if data[0] == '[' {
		// Defense-in-depth for pre-CHECK / bypass writers: never fail Scan.
		slog.Warn("model_routing_array_coerced",
			"detail", "groups.model_routing JSON array coerced to empty object",
			"prefix", summarizeJSONPrefix(data),
		)
		*m = GroupModelRouting{}
		return nil
	}
	if data[0] != '{' {
		return fmt.Errorf("group model_routing: want JSON object, got %s", summarizeJSONPrefix(data))
	}
	var raw map[string][]int64
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	*m = GroupModelRouting(raw)
	return nil
}

// MarshalJSON always emits a JSON object (never an array).
func (m GroupModelRouting) MarshalJSON() ([]byte, error) {
	if m == nil {
		return []byte("null"), nil
	}
	return json.Marshal(map[string][]int64(m))
}

// NormalizeGroupModelRouting returns a non-nil empty map when routing is enabled
// with a nil map, so writers persist '{}' rather than SQL NULL accidentally
// paired with enabled=true. Arrays cannot appear in this Go type.
func NormalizeGroupModelRouting(enabled bool, routing map[string][]int64) map[string][]int64 {
	if routing != nil {
		return routing
	}
	if enabled {
		return map[string][]int64{}
	}
	return nil
}

// ParseGroupModelRoutingJSON validates admin/API raw JSON for model_routing.
// Arrays are rejected (callers must not persist []); objects/null are accepted.
func ParseGroupModelRoutingJSON(raw json.RawMessage) (GroupModelRouting, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	trimmed := bytes.TrimSpace(raw)
	if trimmed[0] == '[' {
		return nil, fmt.Errorf("model_routing must be a JSON object, not an array")
	}
	var out GroupModelRouting
	if err := json.Unmarshal(trimmed, &out); err != nil {
		return nil, fmt.Errorf("invalid model_routing: %w", err)
	}
	return out, nil
}

func summarizeJSONPrefix(data []byte) string {
	if len(data) > 32 {
		return string(data[:32]) + "…"
	}
	return string(data)
}
