package migrations

import (
	"strings"
	"testing"
)

func TestMigrationTK098GroupsModelRoutingObjectShape(t *testing.T) {
	t.Parallel()
	content, err := FS.ReadFile("tk_098_groups_model_routing_object_shape.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(content)
	for _, needle := range []string{
		"jsonb_typeof(model_routing) = 'array'",
		"model_routing = '{}'::jsonb",
		"groups_model_routing_object_check",
		"jsonb_typeof(model_routing) = 'object'",
	} {
		if !strings.Contains(sql, needle) {
			t.Fatalf("migration missing %q", needle)
		}
	}
}
